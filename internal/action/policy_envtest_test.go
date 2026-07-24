// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package action_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/action"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

// This suite is the proof behind the CLI's security claim: the custom verbs are
// enforced by the API server, not by the client. Every case drives a real
// kube-apiserver with the ValidatingAdmissionPolicy installed, as an
// impersonated user holding exactly one verb.
//
// It also catches a policy that does not compile, which unit tests cannot: a
// CEL typo would otherwise ship and only fail against a live cluster.

const (
	policyNamespace = "patchy"
	// policyRelPath is the shipped manifest, not a test copy. Testing a
	// duplicate would prove only that the duplicate works.
	policyRelPath = "../../deploy/kustomize/base/admission-policy.yaml"
)

// policyEnv is a running API server with the CRDs and the admission policy.
type policyEnv struct {
	cfg   *rest.Config
	admin client.Client
}

// startPolicyEnv boots envtest with the CRDs and applies the shipped policy.
func startPolicyEnv(t *testing.T) *policyEnv {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; skipping admission policy envtest")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../deploy/kustomize/base/crds"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	admin, err := client.New(cfg, client.Options{Scheme: kube.Scheme()})
	if err != nil {
		t.Fatalf("build admin client: %v", err)
	}
	ctx := t.Context()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: policyNamespace,
		// The binding selects the namespace by this label, which kubelet-free
		// envtest does not add automatically.
		Labels: map[string]string{"kubernetes.io/metadata.name": policyNamespace},
	}}
	if err := admin.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}
	env2 := &policyEnv{cfg: cfg, admin: admin}
	applyPolicy(t, admin)
	env2.waitForPolicy(t)
	return env2
}

// applyPolicy installs the shipped admission-policy manifest and waits for the
// API server to report it compiled. Without the wait a fast test can write
// before the policy is live and see a false pass.
func applyPolicy(t *testing.T, admin client.Client) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(policyRelPath))
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}

	var objs []*unstructured.Unstructured
	dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(string(raw)), 4096)
	for {
		u := &unstructured.Unstructured{}
		if err := dec.Decode(u); err != nil {
			break
		}
		if len(u.Object) == 0 {
			continue
		}
		objs = append(objs, u)
	}
	if len(objs) != 2 {
		t.Fatalf("expected a policy and a binding in %s, decoded %d documents", policyRelPath, len(objs))
	}
	for _, u := range objs {
		if err := admin.Create(t.Context(), u); err != nil && !apierrors.IsAlreadyExists(err) {
			// A CEL compile error surfaces here, which is exactly what this
			// suite exists to catch before the manifest reaches a cluster.
			t.Fatalf("apply %s %s: %v", u.GetKind(), u.GetName(), err)
		}
	}
}

// waitForPolicy blocks until the policy is actually being enforced.
//
// It probes rather than reading policy status: envtest runs only the API server
// and etcd, so nothing populates the type-checking status a real cluster would
// show. Probing is the stronger check anyway — it proves the admission chain is
// rejecting, which is the property every test below depends on. Without it a
// fast test can write during the propagation window and record a false pass.
func (e *policyEnv) waitForPolicy(t *testing.T) {
	t.Helper()
	probe := e.userClient(t, "probe@acme.test") // granted no custom verbs at all
	e.seed(t, "policy-probe", v1alpha1.PhaseQueued)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		f := get(t, probe, "policy-probe")
		f.Spec.Suspend = !f.Spec.Suspend
		if err := probe.Update(t.Context(), f); err != nil {
			return // the policy is live
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("admission policy never took effect: a user with no verbs could still write spec.suspend")
}

// userClient returns a client impersonating a user granted exactly the given
// custom verbs, plus the native reads and writes a CLI user needs.
func (e *policyEnv) userClient(t *testing.T, username string, verbs ...string) client.Client {
	t.Helper()
	ctx := t.Context()
	safe := strings.NewReplacer("@", "-", ".", "-", ":", "-").Replace(username)

	rules := []rbacv1.PolicyRule{{
		APIGroups: []string{v1alpha1.GroupVersion.Group},
		Resources: []string{"findings"},
		// update is the native verb the API server checks; the policy is what
		// narrows it down to individual fields.
		Verbs: append([]string{"get", "list", "update", "patch"}, verbs...),
	}}
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "role-" + safe, Namespace: policyNamespace},
		Rules:      rules,
	}
	if err := e.admin.Create(ctx, role); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create role: %v", err)
	}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "bind-" + safe, Namespace: policyNamespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: username}},
	}
	if err := e.admin.Create(ctx, binding); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create rolebinding: %v", err)
	}

	cfg := rest.CopyConfig(e.cfg)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: username}
	c, err := client.New(cfg, client.Options{Scheme: kube.Scheme()})
	if err != nil {
		t.Fatalf("build user client: %v", err)
	}
	return c
}

// seed creates a finding as the admin (who is exempt from nothing but holds
// every native verb) and returns it.
func (e *policyEnv) seed(t *testing.T, name string, phase v1alpha1.Phase) *v1alpha1.Finding {
	t.Helper()
	ctx := t.Context()
	f := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: policyNamespace,
			Labels:    map[string]string{v1alpha1.LabelSeverity: "high"},
			Finalizers: []string{
				v1alpha1.FinalizerRollupTotal,
			},
		},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "github-code-scanning",
			Advisories:     []string{"CVE-2026-0001"},
			Title:          "Command injection",
			Severity:       v1alpha1.LevelHigh,
		},
	}
	if err := e.admin.Create(ctx, f); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	f.Status.Phase = phase
	if err := e.admin.Status().Update(ctx, f); err != nil {
		t.Fatalf("seed status %s: %v", name, err)
	}
	t.Cleanup(func() {
		cur := &v1alpha1.Finding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: policyNamespace}}
		_ = e.admin.Patch(context.Background(), cur, client.RawPatch(
			"application/merge-patch+json", []byte(`{"metadata":{"finalizers":null}}`)))
		_ = e.admin.Delete(context.Background(), cur)
	})
	return f
}

// get re-reads a finding through the given client.
func get(t *testing.T, c client.Client, name string) *v1alpha1.Finding {
	t.Helper()
	var f v1alpha1.Finding
	key := client.ObjectKey{Namespace: policyNamespace, Name: name}
	if err := c.Get(t.Context(), key, &f); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return &f
}

// TestPolicyEnforcesVerbPerField is the core claim: a grant of one verb moves
// exactly one field, and nothing else.
func TestPolicyEnforcesVerbPerField(t *testing.T) {
	env := startPolicyEnv(t)

	cases := []struct {
		name      string
		granted   string
		mutate    func(*v1alpha1.Finding)
		wantAllow bool
	}{
		{
			name:      "suspend verb may set spec.suspend",
			granted:   action.VerbSuspend,
			mutate:    func(f *v1alpha1.Finding) { f.Spec.Suspend = true },
			wantAllow: true,
		},
		{
			name:      "suspend verb may not forge an approval",
			granted:   action.VerbSuspend,
			mutate:    func(f *v1alpha1.Finding) { f.Spec.Approval = approval() },
			wantAllow: false,
		},
		{
			name:      "approve verb may set spec.approval",
			granted:   action.VerbApprove,
			mutate:    func(f *v1alpha1.Finding) { f.Spec.Approval = approval() },
			wantAllow: true,
		},
		{
			name:      "approve verb may not suspend",
			granted:   action.VerbApprove,
			mutate:    func(f *v1alpha1.Finding) { f.Spec.Suspend = true },
			wantAllow: false,
		},
		{
			name:      "expedite verb may set spec.expedite",
			granted:   action.VerbExpedite,
			mutate:    func(f *v1alpha1.Finding) { f.Spec.Expedite = request() },
			wantAllow: true,
		},
		{
			name:      "retry verb may set spec.retry",
			granted:   action.VerbRetry,
			mutate:    func(f *v1alpha1.Finding) { f.Spec.Retry = request() },
			wantAllow: true,
		},
		{
			name:      "expedite verb may not retry",
			granted:   action.VerbExpedite,
			mutate:    func(f *v1alpha1.Finding) { f.Spec.Retry = request() },
			wantAllow: false,
		},
		{
			// Every verb in the world does not add up to permission to rewrite
			// the finding the scanner reported.
			name:      "no verb permits rewriting the severity",
			granted:   action.VerbApprove,
			mutate:    func(f *v1alpha1.Finding) { f.Spec.Severity = v1alpha1.LevelLow },
			wantAllow: false,
		},
		{
			name:      "no verb permits rewriting the advisories",
			granted:   action.VerbApprove,
			mutate:    func(f *v1alpha1.Finding) { f.Spec.Advisories = []string{"CVE-2000-0000"} },
			wantAllow: false,
		},
		{
			name:    "no verb permits stripping a rollup finalizer",
			granted: action.VerbSuspend,
			mutate: func(f *v1alpha1.Finding) {
				f.Finalizers = nil
				f.Spec.Suspend = true
			},
			wantAllow: false,
		},
		{
			name:    "no verb permits rewriting the selector labels",
			granted: action.VerbSuspend,
			mutate: func(f *v1alpha1.Finding) {
				f.Labels[v1alpha1.LabelSeverity] = "low"
				f.Spec.Suspend = true
			},
			wantAllow: false,
		},
		{
			// spec.related is documented as human-writable and carries no
			// authority, so it stays editable with plain update.
			name:    "relationship edges stay editable",
			granted: action.VerbSuspend,
			mutate: func(f *v1alpha1.Finding) {
				f.Spec.Related = []v1alpha1.RelatedFinding{{
					From: f.Name, To: "other", Relationship: v1alpha1.RelationshipRelatedTo,
				}}
			},
			wantAllow: true,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := findingName(i)
			env.seed(t, name, v1alpha1.PhaseAwaitingApproval)
			user := env.userClient(t, "user-"+tc.granted+"@acme.test", tc.granted)

			f := get(t, user, name)
			tc.mutate(f)
			err := user.Update(t.Context(), f)

			switch {
			case tc.wantAllow && err != nil:
				t.Errorf("update rejected but should be allowed: %v", err)
			case !tc.wantAllow && err == nil:
				t.Error("update accepted; the admission policy did not deny it")
			case !tc.wantAllow && !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err):
				t.Errorf("update failed for the wrong reason: %v", err)
			}
		})
	}
}

// TestPolicyCannotBeBypassedByPatch covers the obvious end-run: the CLI is not
// the enforcement point, so going around it must change nothing.
func TestPolicyCannotBeBypassedByPatch(t *testing.T) {
	env := startPolicyEnv(t)
	env.seed(t, "patch-target", v1alpha1.PhaseAwaitingApproval)
	user := env.userClient(t, "patcher@acme.test", action.VerbSuspend)

	target := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: "patch-target", Namespace: policyNamespace},
	}
	patch := []byte(`{"spec":{"approval":{"by":"patcher@acme.test","at":"2026-07-24T12:00:00Z"}}}`)
	err := user.Patch(t.Context(), target, client.RawPatch("application/merge-patch+json", patch))
	if err == nil {
		t.Fatal("kubectl-style merge patch forged an approval; the policy only covers full updates")
	}
	if !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
		t.Errorf("patch failed for the wrong reason: %v", err)
	}
}

// TestPolicyCoversCreate closes the delete-and-recreate route: a user who can
// create findings must not be able to create one with an approval already on it.
func TestPolicyCoversCreate(t *testing.T) {
	env := startPolicyEnv(t)
	user := env.userClient(t, "creator@acme.test", action.VerbSuspend, "create")

	f := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: "forged", Namespace: policyNamespace},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "github-code-scanning",
			Advisories:     []string{"CVE-2026-0002"},
			Approval:       approval(),
		},
	}
	err := user.Create(t.Context(), f)
	if err == nil {
		t.Cleanup(func() { _ = env.admin.Delete(context.Background(), f) })
		t.Fatal("created a finding with a pre-set approval; delete-and-recreate laundering is open")
	}
	if !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
		t.Errorf("create failed for the wrong reason: %v", err)
	}
}

// TestPolicyLeavesStatusAlone: the phase machine lives in status, is written
// only by controllers, and must not be caught by a policy aimed at spec.
func TestPolicyLeavesStatusAlone(t *testing.T) {
	env := startPolicyEnv(t)
	f := env.seed(t, "status-target", v1alpha1.PhaseQueued)

	cur := get(t, env.admin, f.Name)
	cur.Status.Phase = v1alpha1.PhaseRemediating
	if err := env.admin.Status().Update(t.Context(), cur); err != nil {
		t.Fatalf("controller status write was blocked by the policy: %v", err)
	}
}

// TestPolicyAllowsTheControllers guards the exemption. If this fails the
// pipeline stops: ingest and accumulation both write spec.
func TestPolicyAllowsTheControllers(t *testing.T) {
	env := startPolicyEnv(t)
	env.seed(t, "controller-target", v1alpha1.PhaseOpened)

	// envtest installs no patchy RBAC, so the SA needs its normal grant first —
	// otherwise plain RBAC denies the write before admission ever runs and the
	// test would pass for the wrong reason.
	c := env.userClient(t, "system:serviceaccount:patchy:patchy-integration-controller")

	cur := get(t, env.admin, "controller-target")
	// Accumulation: exactly the kind of spec write a user may never make.
	cur.Spec.Alerts = append(cur.Spec.Alerts, v1alpha1.Alert{ID: "42"})
	cur.Spec.Severity = v1alpha1.LevelCritical
	if err := c.Update(t.Context(), cur); err != nil {
		t.Fatalf("integration-controller was blocked from writing spec: %v", err)
	}
}

// approval builds a plausible forged approval.
func approval() *v1alpha1.Approval {
	return &v1alpha1.Approval{By: "someone@acme.test", At: metav1.NewTime(time.Now().UTC().Truncate(time.Second))}
}

// request builds a retry/expedite request.
func request() *v1alpha1.ActionRequest {
	return &v1alpha1.ActionRequest{By: "someone@acme.test", At: metav1.NewTime(time.Now().UTC().Truncate(time.Second))}
}

// findingName gives each case its own object so one denial cannot mask another.
func findingName(i int) string {
	return "case-" + strings.ToLower(string(rune('a'+i)))
}
