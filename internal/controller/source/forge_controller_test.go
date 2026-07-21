// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package source

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/forge"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

func forgeHarness(t *testing.T, objs ...client.Object) (*ForgeReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Forge{}).
		Build()
	return &ForgeReconciler{Client: c, Forges: forge.NewStore(c)}, c
}

func TestForgeReconcile(t *testing.T) {
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "patchy"},
		Data:       map[string][]byte{forge.SecretKeyToken: []byte("ghp_dev")},
	}
	emptySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "patchy"},
		Data:       map[string][]byte{},
	}

	cases := []struct {
		name       string
		objs       []client.Object
		wantReady  metav1.ConditionStatus
		wantReason string
	}{
		{"valid token secret", []client.Object{testForge("gh"), tokenSecret},
			metav1.ConditionTrue, "CredentialValid"},
		{"missing secret", []client.Object{testForge("gh")}, metav1.ConditionFalse, "CredentialInvalid"},
		{"secret with no usable keys", []client.Object{testForge("gh"), emptySecret},
			metav1.ConditionFalse, "CredentialInvalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, cl := forgeHarness(t, c.objs...)
			res, err := r.Reconcile(t.Context(), ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "gh"},
			})
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if res.RequeueAfter != defaultRevalidate {
				t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, defaultRevalidate)
			}
			var f v1alpha1.Forge
			if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "gh"}, &f); err != nil {
				t.Fatalf("Get: %v", err)
			}
			cond := meta.FindStatusCondition(f.Status.Conditions, v1alpha1.ConditionReady)
			if cond == nil || cond.Status != c.wantReady || cond.Reason != c.wantReason {
				t.Errorf("Ready = %+v, want %s/%s", cond, c.wantReady, c.wantReason)
			}
		})
	}
}

func TestForgeReconcileHonorsInterval(t *testing.T) {
	f := testForge("gh")
	f.Spec.Interval = metav1.Duration{Duration: time.Hour}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "patchy"},
		Data:       map[string][]byte{forge.SecretKeyToken: []byte("t")},
	}
	r, _ := forgeHarness(t, f, secret)
	res, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "gh"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != time.Hour {
		t.Errorf("RequeueAfter = %v, want 1h", res.RequeueAfter)
	}
}

func TestForgeReconcileSuspended(t *testing.T) {
	f := testForge("gh")
	f.Spec.Suspend = true
	r, _ := forgeHarness(t, f)
	res, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "gh"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v for suspended forge, want 0", res.RequeueAfter)
	}
}
