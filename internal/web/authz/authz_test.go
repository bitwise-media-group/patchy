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
// (user, verb) and calls counts reviews.
func sarClient(t *testing.T, calls *int, allow func(user, verb string) bool) client.Client {
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
				if sar.Spec.ResourceAttributes.Resource != "findings" {
					t.Errorf("review resource = %q, want findings", sar.Spec.ResourceAttributes.Resource)
				}
				sar.Status.Allowed = allow(sar.Spec.User, sar.Spec.ResourceAttributes.Verb)
				return nil
			},
		}).
		Build()
}

func TestReviewerGrants(t *testing.T) {
	cases := []struct {
		name      string
		allow     func(user, verb string) bool
		wantView  bool
		wantVerbs []string
	}{
		{
			name:     "viewer only",
			allow:    func(_, verb string) bool { return verb == "get" },
			wantView: true,
		},
		{
			name:      "approver",
			allow:     func(_, verb string) bool { return verb == "get" || verb == VerbApprove },
			wantView:  true,
			wantVerbs: []string{VerbApprove},
		},
		{
			name:      "operator",
			allow:     func(string, string) bool { return true },
			wantView:  true,
			wantVerbs: []string{VerbApprove, VerbRetry, VerbExpedite, VerbSuspend, VerbResume},
		},
		{
			name:  "nothing",
			allow: func(string, string) bool { return false },
		},
		{
			// A verb grant without view still surfaces: the view gate and
			// the action gate are independent reviews.
			name:      "actions without view",
			allow:     func(_, verb string) bool { return verb == VerbSuspend },
			wantVerbs: []string{VerbSuspend},
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
			if g.View != tc.wantView {
				t.Errorf("View = %v, want %v", g.View, tc.wantView)
			}
			if !slices.Equal(g.Verbs, tc.wantVerbs) {
				t.Errorf("Verbs = %v, want %v", g.Verbs, tc.wantVerbs)
			}
		})
	}
}

func TestReviewerCache(t *testing.T) {
	calls := 0
	r := NewReviewer(sarClient(t, &calls, func(string, string) bool { return true }), "patchy", time.Minute)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }

	id := auth.Identity{Username: "u", Groups: []string{"b", "a"}}
	if _, err := r.Grants(t.Context(), id); err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if calls != 8 {
		t.Fatalf("first resolve made %d reviews, want 8", calls)
	}

	// Same identity with reordered groups hits the cache.
	if _, err := r.Grants(t.Context(), auth.Identity{Username: "u", Groups: []string{"a", "b"}}); err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if calls != 8 {
		t.Errorf("cached resolve made %d extra reviews", calls-8)
	}

	// A different identity misses.
	if _, err := r.Grants(t.Context(), auth.Identity{Username: "v"}); err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if calls != 16 {
		t.Errorf("distinct identity made %d reviews total, want 16", calls)
	}

	// Expiry re-resolves.
	now = now.Add(2 * time.Minute)
	if _, err := r.Grants(t.Context(), id); err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if calls != 24 {
		t.Errorf("expired resolve made %d reviews total, want 24", calls)
	}
}

func TestFullGrantsEverything(t *testing.T) {
	g, err := Full{}.Grants(t.Context(), auth.Identity{})
	if err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if !g.View || !slices.Equal(g.Verbs, ActionVerbs) {
		t.Errorf("Full grants = %+v, want view + all verbs", g)
	}
	for _, verb := range ActionVerbs {
		if !g.Allows(verb) {
			t.Errorf("Allows(%s) = false", verb)
		}
	}
}
