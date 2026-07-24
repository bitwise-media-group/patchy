// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package authz

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/bitwise-media-group/patchy/internal/action"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
)

// The verb vocabulary is owned by internal/action, which also holds the
// state-machine gating behind each verb; authz only resolves who may use them.
// Re-exported here so the server keeps reading its grants and its actions from
// one place.
const (
	// VerbApprove is the custom RBAC verb behind the approve action.
	VerbApprove = action.VerbApprove
	// VerbRetry is the custom RBAC verb behind the retry action (recover a
	// Failed finding to the state it failed from).
	VerbRetry = action.VerbRetry
	// VerbExpedite is the custom RBAC verb behind the expedite action (skip
	// the waiting stages: accumulation, minimum age, queue position).
	VerbExpedite = action.VerbExpedite
	// VerbSuspend is the custom RBAC verb behind the suspend action.
	VerbSuspend = action.VerbSuspend
	// VerbResume is the custom RBAC verb behind the resume action.
	VerbResume = action.VerbResume
	// VerbReplay is the custom RBAC verb behind the namespace-wide replay
	// action (redeliver the webhook delivery log — demo tooling).
	VerbReplay = action.VerbReplay
	// VerbReset is the custom RBAC verb behind the namespace-wide reset
	// action (delete every pipeline resource — demo tooling).
	VerbReset = action.VerbReset
)

// ActionVerbs lists the per-finding custom verbs in the order the UI
// receives them.
var ActionVerbs = action.ActionVerbs

// AdminVerbs lists the namespace-wide custom verbs, resolved the same way
// but surfaced on the dataset's user rather than per finding.
var AdminVerbs = action.AdminVerbs

// findingsGroup is the API group carrying the custom verbs.
const findingsGroup = "patchy.bitwisemedia.uk"

// defaultTTL bounds how stale a cached grant may be. Short on purpose: a
// revoked approver should lose the buttons within seconds, and four SARs
// per user per window are cheap.
const defaultTTL = 20 * time.Second

// cacheLimit bounds the grant cache; at the limit the cache resets rather
// than evicting piecemeal — grants rebuild in one round of reviews.
const cacheLimit = 1024

// Grants is what one identity may do in the server's namespace. RBAC is
// namespace-scoped, so grants apply uniformly across findings.
type Grants struct {
	// View reports native get on findings — the dashboard read gate.
	View bool
	// Verbs are the granted custom action verbs, in ActionVerbs order.
	Verbs []string
	// Admin are the granted namespace-wide verbs, in AdminVerbs order.
	Admin []string
}

// Allows reports whether the per-finding verb is granted.
func (g Grants) Allows(verb string) bool {
	return slices.Contains(g.Verbs, verb)
}

// AllowsAdmin reports whether the namespace-wide verb is granted.
func (g Grants) AllowsAdmin(verb string) bool {
	return slices.Contains(g.Admin, verb)
}

// Full is the bypass granter for auth mode none: everything, for everyone.
type Full struct{}

// Grants returns every grant.
func (Full) Grants(context.Context, auth.Identity) (Grants, error) {
	return Grants{
		View:  true,
		Verbs: append([]string(nil), ActionVerbs...),
		Admin: append([]string(nil), AdminVerbs...),
	}, nil
}

// Reviewer resolves grants through SubjectAccessReviews, cached briefly per
// identity.
type Reviewer struct {
	client    client.Client
	namespace string
	ttl       time.Duration
	now       func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	grants  Grants
	expires time.Time
}

// NewReviewer builds a Reviewer for the server's namespace. ttl <= 0 uses
// the default.
func NewReviewer(c client.Client, namespace string, ttl time.Duration) *Reviewer {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &Reviewer{
		client:    c,
		namespace: namespace,
		ttl:       ttl,
		now:       time.Now,
		cache:     make(map[string]cached),
	}
}

// Grants resolves the identity's grants: native get on findings for View,
// then one review per custom verb.
func (r *Reviewer) Grants(ctx context.Context, id auth.Identity) (Grants, error) {
	key := cacheKey(id)
	r.mu.Lock()
	if hit, ok := r.cache[key]; ok && r.now().Before(hit.expires) {
		r.mu.Unlock()
		return hit.grants, nil
	}
	r.mu.Unlock()

	var g Grants
	view, err := r.review(ctx, id, "get")
	if err != nil {
		return Grants{}, err
	}
	g.View = view
	for _, verb := range ActionVerbs {
		allowed, err := r.review(ctx, id, verb)
		if err != nil {
			return Grants{}, err
		}
		if allowed {
			g.Verbs = append(g.Verbs, verb)
		}
	}
	for _, verb := range AdminVerbs {
		allowed, err := r.review(ctx, id, verb)
		if err != nil {
			return Grants{}, err
		}
		if allowed {
			g.Admin = append(g.Admin, verb)
		}
	}

	r.mu.Lock()
	if len(r.cache) >= cacheLimit {
		r.cache = make(map[string]cached)
	}
	r.cache[key] = cached{grants: g, expires: r.now().Add(r.ttl)}
	r.mu.Unlock()
	return g, nil
}

// review runs one SubjectAccessReview for verb on findings.
func (r *Reviewer) review(ctx context.Context, id auth.Identity, verb string) (bool, error) {
	sar := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   id.Username,
			Groups: id.Groups,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: r.namespace,
				Group:     findingsGroup,
				Resource:  "findings",
				Verb:      verb,
			},
		},
	}
	if err := r.client.Create(ctx, sar); err != nil {
		return false, fmt.Errorf("access review %s findings for %s: %w", verb, id.Username, err)
	}
	return sar.Status.Allowed, nil
}

// cacheKey folds the identity into a stable cache key; groups are order-
// insensitive.
func cacheKey(id auth.Identity) string {
	groups := append([]string(nil), id.Groups...)
	slices.Sort(groups)
	return id.Username + "\x00" + strings.Join(groups, "\x00")
}
