// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package authz

import (
	"context"
	"slices"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
)

// sarClient fabricates SubjectAccessReview responses: allow decides per
// (user, resource, verb) and calls counts reviews.
func sarClient(t *testing.T, calls *int, allow func(user, resource, verb string) bool) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
				sar, ok := obj.(*authorizationv1.SubjectAccessReview)
				if !ok {
					t.Fatalf("unexpected create of %T", obj)
				}
				*calls++
				if res := sar.Spec.ResourceAttributes.Resource; res != "findings" && res != "integrations" {
					t.Errorf("review resource = %q, want findings or integrations", res)
				}
				sar.Status.Allowed = allow(sar.Spec.User, sar.Spec.ResourceAttributes.Resource,
					sar.Spec.ResourceAttributes.Verb)
				return nil
			},
		}).
		Build()
}

func TestReviewerGrants(t *testing.T) {
	cases := []struct {
		name            string
		allow           func(user, resource, verb string) bool
		wantView        bool
		wantConfig      bool
		wantVerbs       []string
		wantIntegration []string
		wantAdmin       []string
	}{
		{
			name:     "viewer only",
			allow:    func(_, resource, verb string) bool { return resource == "findings" && verb == "get" },
			wantView: true,
		},
		{
			name: "approver",
			allow: func(_, resource, verb string) bool {
				return resource == "findings" && (verb == "get" || verb == VerbApprove)
			},
			wantView:  true,
			wantVerbs: []string{VerbApprove},
		},
		{
			name:            "operator",
			allow:           func(string, string, string) bool { return true },
			wantView:        true,
			wantConfig:      true,
			wantVerbs:       []string{VerbApprove, VerbRetry, VerbExpedite, VerbSuspend, VerbResume},
			wantIntegration: []string{VerbBackfill, VerbReplay, VerbReset},
			wantAdmin:       []string{VerbReplay, VerbReset},
		},
		{
			name:  "nothing",
			allow: func(string, string, string) bool { return false },
		},
		{
			// A verb grant without view still surfaces: the view gate and
			// the action gate are independent reviews.
			name:      "actions without view",
			allow:     func(_, resource, verb string) bool { return resource == "findings" && verb == VerbSuspend },
			wantVerbs: []string{VerbSuspend},
		},
		{
			// The integration-scoped verbs resolve on integrations, never
			// on findings: a findings-only grant of "backfill" is inert.
			name: "integration verbs need the integrations resource",
			allow: func(_, resource, verb string) bool {
				return resource == "findings" && verb == VerbBackfill
			},
		},
		{
			name: "config view plus backfill only",
			allow: func(_, resource, verb string) bool {
				return resource == "integrations" && (verb == "get" || verb == VerbBackfill)
			},
			wantConfig:      true,
			wantIntegration: []string{VerbBackfill},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := NewReviewer(sarClient(t, &calls, tc.allow), "patchy", 0)
			g, err := r.Grants(t.Context(), auth.Identity{Username: "u"})
			if err != nil {
				t.Fatalf("Grants: %v", err)
			}
			if g.View != tc.wantView || g.Config != tc.wantConfig {
				t.Errorf("View/Config = %v/%v, want %v/%v", g.View, g.Config, tc.wantView, tc.wantConfig)
			}
			if !slices.Equal(g.Verbs, tc.wantVerbs) {
				t.Errorf("Verbs = %v, want %v", g.Verbs, tc.wantVerbs)
			}
			if !slices.Equal(g.Integration, tc.wantIntegration) {
				t.Errorf("Integration = %v, want %v", g.Integration, tc.wantIntegration)
			}
			if !slices.Equal(g.Admin, tc.wantAdmin) {
				t.Errorf("Admin = %v, want %v", g.Admin, tc.wantAdmin)
			}
		})
	}
}

func TestReviewerCache(t *testing.T) {
	calls := 0
	r := NewReviewer(sarClient(t, &calls, func(string, string, string) bool { return true }), "patchy", time.Minute)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }

	id := auth.Identity{Username: "u", Groups: []string{"b", "a"}}
	if _, err := r.Grants(t.Context(), id); err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if calls != 10 {
		t.Fatalf("first resolve made %d reviews, want 10", calls)
	}

	// Same identity with reordered groups hits the cache.
	if _, err := r.Grants(t.Context(), auth.Identity{Username: "u", Groups: []string{"a", "b"}}); err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if calls != 10 {
		t.Errorf("cached resolve made %d extra reviews", calls-10)
	}

	// A different identity misses.
	if _, err := r.Grants(t.Context(), auth.Identity{Username: "v"}); err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if calls != 20 {
		t.Errorf("distinct identity made %d reviews total, want 20", calls)
	}

	// Expiry re-resolves.
	now = now.Add(2 * time.Minute)
	if _, err := r.Grants(t.Context(), id); err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if calls != 30 {
		t.Errorf("expired resolve made %d reviews total, want 30", calls)
	}
}

func TestFullGrantsEverything(t *testing.T) {
	g, err := Full{}.Grants(t.Context(), auth.Identity{})
	if err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if !g.View || !g.Config || !slices.Equal(g.Verbs, ActionVerbs) ||
		!slices.Equal(g.Integration, IntegrationVerbs) || !slices.Equal(g.Admin, AdminVerbs) {
		t.Errorf("Full grants = %+v, want everything", g)
	}
	for _, verb := range ActionVerbs {
		if !g.Allows(verb) {
			t.Errorf("Allows(%s) = false", verb)
		}
	}
	for _, verb := range IntegrationVerbs {
		if !g.AllowsIntegration(verb) {
			t.Errorf("AllowsIntegration(%s) = false", verb)
		}
	}
}
