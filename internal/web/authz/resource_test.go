// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package authz

import (
	"context"
	"errors"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
)

// resourceSARClient fabricates SubjectAccessReview responses for the
// resource-parameterized reviewer, checking the attributes each review carries.
func resourceSARClient(t *testing.T, calls *int, allow func(user, verb string) bool) client.Client {
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
				attrs := sar.Spec.ResourceAttributes
				if attrs.Namespace != "patchy" || attrs.Group != "patchy.bitwisemedia.uk" || attrs.Resource != "evaluations" {
					t.Errorf("review attributes = %s/%s/%s, want patchy/patchy.bitwisemedia.uk/evaluations",
						attrs.Namespace, attrs.Group, attrs.Resource)
				}
				sar.Status.Allowed = allow(sar.Spec.User, attrs.Verb)
				return nil
			},
		}).
		Build()
}

func TestResourceReviewerAllowed(t *testing.T) {
	calls := 0
	c := resourceSARClient(t, &calls, func(user, verb string) bool {
		return user == "dev" && verb != "delete"
	})
	r := NewResourceReviewer(c, "patchy", "patchy.bitwisemedia.uk", "evaluations", time.Minute)

	cases := []struct {
		name string
		id   auth.Identity
		verb string
		want bool
	}{
		{"creator may create", auth.Identity{Username: "dev"}, "create", true},
		{"creator may not delete", auth.Identity{Username: "dev"}, "delete", false},
		{"stranger denied", auth.Identity{Username: "mallory"}, "create", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := r.Allowed(t.Context(), c.id, c.verb)
			if err != nil {
				t.Fatalf("Allowed: %v", err)
			}
			if got != c.want {
				t.Errorf("Allowed(%s, %s) = %t, want %t", c.id.Username, c.verb, got, c.want)
			}
		})
	}
}

func TestResourceReviewerCache(t *testing.T) {
	calls := 0
	r := NewResourceReviewer(resourceSARClient(t, &calls, func(string, string) bool { return true }),
		"patchy", "patchy.bitwisemedia.uk", "evaluations", time.Minute)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }

	id := auth.Identity{Username: "dev", Groups: []string{"eng"}}
	if _, err := r.Allowed(t.Context(), id, "get"); err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("first check made %d reviews, want 1", calls)
	}

	// Same identity+verb hits the cache.
	if _, err := r.Allowed(t.Context(), id, "get"); err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if calls != 1 {
		t.Errorf("cached check made %d extra reviews", calls-1)
	}

	// A different verb misses.
	if _, err := r.Allowed(t.Context(), id, "create"); err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if calls != 2 {
		t.Errorf("distinct verb made %d reviews total, want 2", calls)
	}

	// Expiry re-reviews.
	now = now.Add(2 * time.Minute)
	if _, err := r.Allowed(t.Context(), id, "get"); err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if calls != 3 {
		t.Errorf("expired check made %d reviews total, want 3", calls)
	}
}

func TestResourceReviewerError(t *testing.T) {
	boom := errors.New("apiserver down")
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return boom
			},
		}).
		Build()
	r := NewResourceReviewer(c, "patchy", "patchy.bitwisemedia.uk", "evaluations", 0)
	if _, err := r.Allowed(t.Context(), auth.Identity{Username: "dev"}, "get"); !errors.Is(err, boom) {
		t.Fatalf("Allowed error = %v, want wrapped %v", err, boom)
	}
}
