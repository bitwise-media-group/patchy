// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package tokenauth

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PodNameExtraKey is where the API server reports the bound pod of a
// pod-bound projected token on the TokenReview user info.
const PodNameExtraKey = "authentication.kubernetes.io/pod-name"

// DefaultTTL is how long a verdict is cached when Config.TTL is unset.
const DefaultTTL = time.Minute

// Config configures an Authenticator.
type Config struct {
	// Audience is the projected-token audience callers must present.
	Audience string
	// Subjects are the only accepted usernames, e.g.
	// system:serviceaccount:patchy:patchy-status-server.
	Subjects []string
	// TTL is how long one token's verdict is cached; <= 0 means DefaultTTL.
	TTL time.Duration
}

// ServiceAccount renders the TokenReview username of a ServiceAccount.
func ServiceAccount(namespace, name string) string {
	return fmt.Sprintf("system:serviceaccount:%s:%s", namespace, name)
}

// Identity is who a validated caller token belongs to.
type Identity struct {
	// User is the TokenReview username.
	User string
	// Pod is the bound pod name, for audit; empty for a token not bound to
	// a pod.
	Pod string
}

// verdict is one cached TokenReview outcome.
type verdict struct {
	id      Identity
	allowed bool
	reason  string
	expiry  time.Time
}

// Authenticator validates caller tokens via TokenReview, caching verdicts by
// token hash for the configured TTL.
type Authenticator struct {
	cs       kubernetes.Interface
	audience string
	subjects []string
	ttl      time.Duration
	now      func() time.Time

	mu    sync.Mutex
	cache map[[sha256.Size]byte]verdict
}

// New builds an Authenticator; cs performs the TokenReviews.
func New(cs kubernetes.Interface, cfg Config) *Authenticator {
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Authenticator{
		cs:       cs,
		audience: cfg.Audience,
		subjects: slices.Clone(cfg.Subjects),
		ttl:      ttl,
		now:      time.Now,
		cache:    map[[sha256.Size]byte]verdict{},
	}
}

// Authenticate resolves the token in the request's header to an identity,
// reviewing it with the API server on a cache miss. The error string is safe
// to return to the caller (it never echoes the token).
func (a *Authenticator) Authenticate(r *http.Request, header string) (Identity, error) {
	token := r.Header.Get(header)
	if token == "" {
		return Identity{}, fmt.Errorf("missing %s header", header)
	}
	key := sha256.Sum256([]byte(token))

	a.mu.Lock()
	v, ok := a.cache[key]
	a.mu.Unlock()
	if !ok || a.now().After(v.expiry) {
		v = a.review(r, token)
		a.mu.Lock()
		a.cache[key] = v
		a.prune()
		a.mu.Unlock()
	}
	if !v.allowed {
		return Identity{}, fmt.Errorf("token rejected: %s", v.reason)
	}
	return v.id, nil
}

func (a *Authenticator) review(r *http.Request, token string) verdict {
	expiry := a.now().Add(a.ttl)
	tr, err := a.cs.AuthenticationV1().TokenReviews().Create(r.Context(), &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{Token: token, Audiences: []string{a.audience}},
	}, metav1.CreateOptions{})
	if err != nil {
		// An unreachable API server is not a verdict. Caching it for the full
		// TTL would let a blip lock every caller out for that long.
		return verdict{allowed: false, reason: "token review unavailable", expiry: a.now()}
	}
	switch {
	case !tr.Status.Authenticated:
		return verdict{allowed: false, reason: "not authenticated for audience " + a.audience, expiry: expiry}
	case !slices.Contains(a.subjects, tr.Status.User.Username):
		return verdict{allowed: false, reason: "identity is not an accepted service account", expiry: expiry}
	}
	id := Identity{User: tr.Status.User.Username}
	if pods := tr.Status.User.Extra[PodNameExtraKey]; len(pods) > 0 {
		id.Pod = pods[0]
	}
	return verdict{allowed: true, id: id, expiry: expiry}
}

// prune drops expired entries; called under mu. The cache is naturally small
// (one entry per live caller token), so a sweep on write is enough.
func (a *Authenticator) prune() {
	now := a.now()
	for k, v := range a.cache {
		if now.After(v.expiry) {
			delete(a.cache, k)
		}
	}
}
