// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/internal/action"
)

func testCLIIntegration(mutate ...func(*v1alpha1.Integration)) *v1alpha1.Integration {
	i := &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: testNamespace},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGitHub,
			SecretRef: &v1alpha1.LocalSecretReference{Name: "creds"},
			GitHub: &v1alpha1.GitHubIntegration{
				CodeScanningAlerts: &v1alpha1.GitHubCodeScanningAlerts{Enabled: true},
			},
		},
	}
	for _, m := range mutate {
		m(i)
	}
	return i
}

func TestBackfillStampsSpec(t *testing.T) {
	h := newHarness(t, testCLIIntegration())

	if err := runBackfill(t.Context(), h.opts, "gh", []string{"acme/", "acme/shop"}); err != nil {
		t.Fatalf("runBackfill: %v", err)
	}

	var integ v1alpha1.Integration
	key := types.NamespacedName{Namespace: testNamespace, Name: "gh"}
	if err := h.client.Get(context.Background(), key, &integ); err != nil {
		t.Fatalf("get integration: %v", err)
	}
	req := integ.Spec.Backfill
	if req == nil || req.By != "op@acme.test" {
		t.Fatalf("spec.backfill = %+v, want stamped by op@acme.test", req)
	}
	if want := []string{"acme/", "acme/shop"}; !reflect.DeepEqual(req.Repositories, want) {
		t.Errorf("repositories = %v, want %v", req.Repositories, want)
	}
}

// The access review gates the write, and it asks about the backfill verb on
// integrations — not findings.
func TestBackfillDeniedStopsBeforeWriting(t *testing.T) {
	h := newHarness(t, testCLIIntegration())
	var asked [2]string
	h.opts.WithAccess(func(_ context.Context, _ *kubecfg.Env, resource, verb string) (bool, error) {
		asked = [2]string{resource, verb}
		return false, nil
	})

	err := runBackfill(t.Context(), h.opts, "gh", nil)
	if !errors.Is(err, errDenied) {
		t.Fatalf("err = %v, want errDenied", err)
	}
	if asked != [2]string{"integrations", action.VerbBackfill} {
		t.Errorf("access review asked %v, want [integrations backfill]", asked)
	}

	var integ v1alpha1.Integration
	key := types.NamespacedName{Namespace: testNamespace, Name: "gh"}
	if err := h.client.Get(context.Background(), key, &integ); err != nil {
		t.Fatalf("get integration: %v", err)
	}
	if integ.Spec.Backfill != nil {
		t.Error("spec.backfill written despite the denial")
	}
}

func TestBackfillRejectsBadInput(t *testing.T) {
	h := newHarness(t, testCLIIntegration(func(i *v1alpha1.Integration) { i.Spec.Suspend = true }))

	if err := runBackfill(t.Context(), h.opts, "gh", []string{"acme /shop"}); err == nil {
		t.Error("whitespace --repo entry accepted, want a usage error")
	}
	if err := runBackfill(t.Context(), h.opts, "gh", nil); err == nil {
		t.Error("suspended integration accepted a backfill, want an error")
	}
	if err := runBackfill(t.Context(), h.opts, "nope", nil); err == nil {
		t.Error("unknown integration accepted, want not-found")
	}
}
