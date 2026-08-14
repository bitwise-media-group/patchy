// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package action_test

import (
	"time"

	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/action"
)

// The integrations policy's own envtest proof: the request stamps
// (spec.backfill, spec.replay, spec.reset) each need their own custom verb
// on integrations, while plain update keeps working for everything else in
// IntegrationSpec — the deliberate divergence from the findings policy.

// integrationUserClient impersonates a user granted exactly the given
// custom verbs on integrations, plus the native reads and writes an
// operator holds.
func (e *policyEnv) integrationUserClient(t *testing.T, username string, verbs ...string) client.Client {
	t.Helper()
	ctx := t.Context()
	safe := "integ-" + username

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "role-" + safe, Namespace: policyNamespace},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{v1alpha1.GroupVersion.Group},
			Resources: []string{"integrations"},
			Verbs:     append([]string{"get", "list", "update", "patch"}, verbs...),
		}},
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
	c, err := client.New(cfg, client.Options{Scheme: e.admin.Scheme()})
	if err != nil {
		t.Fatalf("build user client: %v", err)
	}
	return c
}

// seedIntegration creates a github Integration as the admin.
func (e *policyEnv) seedIntegration(t *testing.T, name string, mutate func(*v1alpha1.Integration)) {
	t.Helper()
	integ := &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: policyNamespace},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGitHub,
			SecretRef: &v1alpha1.LocalSecretReference{Name: "creds"},
			GitHub: &v1alpha1.GitHubIntegration{
				CodeScanningAlerts: &v1alpha1.GitHubCodeScanningAlerts{Enabled: true},
			},
		},
	}
	if mutate != nil {
		mutate(integ)
	}
	if err := e.admin.Create(t.Context(), integ); err != nil {
		t.Fatalf("seed integration %s: %v", name, err)
	}
}

// waitForIntegrationPolicy probes until the integrations policy is live,
// exactly like waitForPolicy does for findings — the two policies
// propagate independently.
func (e *policyEnv) waitForIntegrationPolicy(t *testing.T) {
	t.Helper()
	probe := e.integrationUserClient(t, "integ-probe@acme.test") // no custom verbs
	e.seedIntegration(t, "integ-policy-probe", nil)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var integ v1alpha1.Integration
		key := client.ObjectKey{Namespace: policyNamespace, Name: "integ-policy-probe"}
		if err := probe.Get(t.Context(), key, &integ); err != nil {
			t.Fatalf("get probe integration: %v", err)
		}
		integ.Spec.Backfill = &v1alpha1.BackfillRequest{By: "probe", At: metav1.Now()}
		if err := probe.Update(t.Context(), &integ); err != nil {
			return // the policy is live
		}
		// The write went through: undo it and retry.
		integ.Spec.Backfill = nil
		if err := e.admin.Update(t.Context(), &integ); err != nil {
			t.Fatalf("reset probe integration: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("integrations admission policy never took effect: a user with no verbs could stamp spec.backfill")
}

func getIntegration(t *testing.T, c client.Client, name string) *v1alpha1.Integration {
	t.Helper()
	var integ v1alpha1.Integration
	key := client.ObjectKey{Namespace: policyNamespace, Name: name}
	if err := c.Get(t.Context(), key, &integ); err != nil {
		t.Fatalf("get integration %s: %v", name, err)
	}
	return &integ
}

func backfillRequest() *v1alpha1.BackfillRequest {
	return &v1alpha1.BackfillRequest{
		By: "someone@acme.test",
		At: metav1.NewTime(time.Now().UTC().Truncate(time.Second)),
	}
}

// TestIntegrationPolicyEnforcesVerbPerField: one verb moves exactly its
// own request field, plain update keeps the operator surface, and
// clearing a stamped request needs no verb at all.
func TestIntegrationPolicyEnforcesVerbPerField(t *testing.T) {
	env := startPolicyEnv(t)
	env.waitForIntegrationPolicy(t)

	cases := []struct {
		name      string
		granted   []string
		seed      func(*v1alpha1.Integration)
		mutate    func(*v1alpha1.Integration)
		wantAllow bool
	}{
		{
			name:      "backfill verb may set spec.backfill",
			granted:   []string{action.VerbBackfill},
			mutate:    func(i *v1alpha1.Integration) { i.Spec.Backfill = backfillRequest() },
			wantAllow: true,
		},
		{
			name:      "backfill verb may not replay",
			granted:   []string{action.VerbBackfill},
			mutate:    func(i *v1alpha1.Integration) { i.Spec.Replay = request() },
			wantAllow: false,
		},
		{
			name:      "replay verb may set spec.replay",
			granted:   []string{action.VerbReplay},
			mutate:    func(i *v1alpha1.Integration) { i.Spec.Replay = request() },
			wantAllow: true,
		},
		{
			name:      "reset verb may set spec.reset",
			granted:   []string{action.VerbReset},
			mutate:    func(i *v1alpha1.Integration) { i.Spec.Reset = request() },
			wantAllow: true,
		},
		{
			name:      "replay verb may not backfill",
			granted:   []string{action.VerbReplay},
			mutate:    func(i *v1alpha1.Integration) { i.Spec.Backfill = backfillRequest() },
			wantAllow: false,
		},
		{
			// The deliberate divergence from the findings policy: operator
			// configuration stays writable with plain update.
			name:    "plain update keeps the operator surface",
			granted: nil,
			mutate: func(i *v1alpha1.Integration) {
				i.Spec.Interval = metav1.Duration{Duration: 30 * time.Minute}
				i.Spec.Suspend = true
				i.Spec.GitHub.Redelivery = &v1alpha1.GitHubRedelivery{Enabled: true}
			},
			wantAllow: true,
		},
		{
			// The second divergence: a GitOps apply pruning a stamped
			// request must not be denied. Cancelling carries no authority.
			name:      "clearing a stamped request needs no verb",
			granted:   nil,
			seed:      func(i *v1alpha1.Integration) { i.Spec.Replay = request() },
			mutate:    func(i *v1alpha1.Integration) { i.Spec.Replay = nil },
			wantAllow: true,
		},
		{
			name:      "an unchanged stamped request passes without the verb",
			granted:   nil,
			seed:      func(i *v1alpha1.Integration) { i.Spec.Backfill = backfillRequest() },
			mutate:    func(i *v1alpha1.Integration) { i.Spec.Suspend = true },
			wantAllow: true,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "integ-case-" + string(rune('a'+i))
			env.seedIntegration(t, name, tc.seed)
			user := env.integrationUserClient(t, "iuser-"+name+"@acme.test", tc.granted...)

			integ := getIntegration(t, user, name)
			tc.mutate(integ)
			err := user.Update(t.Context(), integ)

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

// TestIntegrationPolicyCoversCreate: creating an Integration with a
// request preset needs the verb, closing the delete-and-recreate route.
func TestIntegrationPolicyCoversCreate(t *testing.T) {
	env := startPolicyEnv(t)
	env.waitForIntegrationPolicy(t)
	user := env.integrationUserClient(t, "integ-creator@acme.test", "create")

	integ := &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "integ-forged", Namespace: policyNamespace},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGitHub,
			SecretRef: &v1alpha1.LocalSecretReference{Name: "creds"},
			GitHub:    &v1alpha1.GitHubIntegration{},
			Backfill:  backfillRequest(),
		},
	}
	err := user.Create(t.Context(), integ)
	if err == nil {
		t.Fatal("created an integration with a pre-set backfill request; recreate laundering is open")
	}
	if !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
		t.Errorf("create failed for the wrong reason: %v", err)
	}
}

// TestIntegrationPolicyExemptsTheStatusServer guards the exemption: the
// status server stamps requests as its own ServiceAccount after doing its
// own SubjectAccessReview.
func TestIntegrationPolicyExemptsTheStatusServer(t *testing.T) {
	env := startPolicyEnv(t)
	env.waitForIntegrationPolicy(t)
	env.seedIntegration(t, "integ-sa-target", nil)

	c := env.integrationUserClient(t, "system:serviceaccount:patchy:patchy-status-server")
	integ := getIntegration(t, c, "integ-sa-target")
	integ.Spec.Backfill = backfillRequest()
	if err := c.Update(t.Context(), integ); err != nil {
		t.Fatalf("status-server was blocked from stamping spec.backfill: %v", err)
	}
}
