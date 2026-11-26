// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// backfillRequests lists every BackfillRequest in the test namespace.
func (h *harness) backfillRequests(t *testing.T) []v1alpha1.BackfillRequest {
	t.Helper()
	var list v1alpha1.BackfillRequestList
	if err := h.client.List(context.Background(), &list, client.InNamespace(testNamespace)); err != nil {
		t.Fatalf("list backfill requests: %v", err)
	}
	return list.Items
}

// recordAccess answers every access review with allow(resource, verb) and
// records what was asked, in order.
func (h *harness) recordAccess(allow func(resource, verb string) bool) *[][2]string {
	asked := &[][2]string{}
	h.opts.WithAccess(func(_ context.Context, _ *kubecfg.Env, resource, verb string) (bool, error) {
		*asked = append(*asked, [2]string{resource, verb})
		return allow(resource, verb), nil
	})
	return asked
}

func TestBackfillCreatesRequest(t *testing.T) {
	cases := []struct {
		name    string
		flags   *backfillFlags
		wantVia v1alpha1.BackfillVia
		wantOut []string
	}{
		{
			name:    "a granted backfill runs at once",
			flags:   &backfillFlags{repos: []string{"acme/", "acme/shop"}},
			wantVia: "",
			wantOut: []string{
				"created on integration gh", "patchy get backfillrequests backfill-", "kubectl get backfillrequests",
			},
		},
		{
			name:    "--request files one for an operator",
			flags:   &backfillFlags{repos: []string{"acme/shop"}, request: true},
			wantVia: v1alpha1.BackfillViaRequested,
			wantOut: []string{"AwaitingApproval", "patchy approve backfillrequest backfill-", "patchy get backfillrequests"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testCLIIntegration())
			if err := runBackfill(t.Context(), h.opts, tc.flags, "gh"); err != nil {
				t.Fatalf("runBackfill: %v", err)
			}

			reqs := h.backfillRequests(t)
			if len(reqs) != 1 {
				t.Fatalf("got %d backfill requests, want 1", len(reqs))
			}
			req := reqs[0]
			if !strings.HasPrefix(req.Name, "backfill-") || req.GenerateName != "backfill-" {
				t.Errorf("name = %q (generateName %q), want a generated backfill- name", req.Name, req.GenerateName)
			}
			if got := req.Labels[v1alpha1.LabelIntegration]; got != "gh" {
				t.Errorf("integration label = %q, want gh", got)
			}
			if req.Spec.IntegrationRef.Name != "gh" {
				t.Errorf("integrationRef = %q, want gh", req.Spec.IntegrationRef.Name)
			}
			// The admission policy requires requestedBy to be the creating user.
			if req.Spec.RequestedBy != "op@acme.test" {
				t.Errorf("requestedBy = %q, want the reviewed identity", req.Spec.RequestedBy)
			}
			if req.Spec.Via != tc.wantVia {
				t.Errorf("via = %q, want %q", req.Spec.Via, tc.wantVia)
			}
			if req.Spec.Decision != nil {
				t.Error("the requester recorded a decision on their own request")
			}
			if !reflect.DeepEqual(req.Spec.Repositories, tc.flags.repos) {
				t.Errorf("repositories = %v, want %v", req.Spec.Repositories, tc.flags.repos)
			}

			out := h.out.String()
			if !strings.Contains(out, req.Name) {
				t.Errorf("stdout = %q, want it to name the request %s", out, req.Name)
			}
			for _, want := range tc.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("stdout = %q, want it to contain %q", out, want)
				}
			}
		})
	}
}

// -o name prints just the reference, so a script can capture the request.
func TestBackfillNameOutput(t *testing.T) {
	h := newHarness(t, testCLIIntegration())
	h.opts.Output = "name"
	if err := runBackfill(t.Context(), h.opts, &backfillFlags{}, "gh"); err != nil {
		t.Fatalf("runBackfill: %v", err)
	}
	reqs := h.backfillRequests(t)
	if len(reqs) != 1 {
		t.Fatalf("got %d backfill requests, want 1", len(reqs))
	}
	want := "backfillrequests.patchy.bitwisemedia.uk/" + reqs[0].Name + "\n"
	if got := h.out.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// Without the backfill verb a request that runs at once is refused before
// writing, and the refusal points at --request.
func TestBackfillDeniedSuggestsRequest(t *testing.T) {
	h := newHarness(t, testCLIIntegration())
	asked := h.recordAccess(func(string, string) bool { return false })

	err := runBackfill(t.Context(), h.opts, &backfillFlags{}, "gh")
	if !errors.Is(err, errDenied) {
		t.Fatalf("err = %v, want errDenied", err)
	}
	if got := exitCode(err); got != ExitDenied {
		t.Errorf("exit code = %d, want %d", got, ExitDenied)
	}
	if !strings.Contains(err.Error(), "--request") {
		t.Errorf("error = %q, want it to suggest --request", err)
	}
	if want := [][2]string{{"integrations", action.VerbBackfill}}; !reflect.DeepEqual(*asked, want) {
		t.Errorf("access reviews = %v, want %v", *asked, want)
	}
	if n := len(h.backfillRequests(t)); n != 0 {
		t.Errorf("%d backfill requests created despite the denial", n)
	}
}

// --request needs no backfill verb, only the right to create the request, so
// the verb is never asked about.
func TestBackfillRequestNeedsNoVerb(t *testing.T) {
	h := newHarness(t, testCLIIntegration())
	asked := h.recordAccess(func(resource, _ string) bool { return resource == "backfillrequests" })

	if err := runBackfill(t.Context(), h.opts, &backfillFlags{request: true}, "gh"); err != nil {
		t.Fatalf("runBackfill --request: %v", err)
	}
	if want := [][2]string{{"backfillrequests", "create"}}; !reflect.DeepEqual(*asked, want) {
		t.Errorf("access reviews = %v, want %v", *asked, want)
	}
	reqs := h.backfillRequests(t)
	if len(reqs) != 1 || reqs[0].Spec.Via != v1alpha1.BackfillViaRequested {
		t.Fatalf("requests = %+v, want one filed via requested", reqs)
	}
}

func TestBackfillCreateDenied(t *testing.T) {
	h := newHarness(t, testCLIIntegration())
	h.recordAccess(func(resource, _ string) bool { return resource == "integrations" })

	err := runBackfill(t.Context(), h.opts, &backfillFlags{}, "gh")
	if !errors.Is(err, errDenied) || !strings.Contains(err.Error(), "create backfillrequests") {
		t.Fatalf("err = %v, want a denial naming create on backfillrequests", err)
	}
	if n := len(h.backfillRequests(t)); n != 0 {
		t.Errorf("%d backfill requests created despite the denial", n)
	}
}

func TestBackfillRejectsBadInput(t *testing.T) {
	suspended := testCLIIntegration(func(i *v1alpha1.Integration) { i.Spec.Suspend = true })
	cases := []struct {
		name     string
		flags    *backfillFlags
		target   string
		allNS    bool
		wantCode int
		wantErr  string
	}{
		{
			name:     "whitespace in a --repo entry",
			flags:    &backfillFlags{repos: []string{"acme /shop"}},
			target:   "gh",
			wantCode: ExitUsage,
		},
		{
			name:     "an empty --repo entry",
			flags:    &backfillFlags{repos: []string{" "}},
			target:   "gh",
			wantCode: ExitUsage,
		},
		{
			name:     "a suspended integration",
			flags:    &backfillFlags{},
			target:   "gh",
			wantCode: ExitError,
			wantErr:  "suspended",
		},
		{
			name:     "a suspended integration, by request",
			flags:    &backfillFlags{request: true},
			target:   "gh",
			wantCode: ExitError,
			wantErr:  "suspended",
		},
		{
			name:     "an unknown integration",
			flags:    &backfillFlags{},
			target:   "nope",
			wantCode: ExitNotFound,
		},
		{
			name:     "every namespace at once",
			flags:    &backfillFlags{},
			target:   "gh",
			allNS:    true,
			wantCode: ExitUsage,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, suspended)
			if tc.allNS {
				h.opts.WithEnv(&kubecfg.Env{Client: h.client, Namespace: ""})
			}
			err := runBackfill(t.Context(), h.opts, tc.flags, tc.target)
			if got := exitCode(err); got != tc.wantCode {
				t.Fatalf("exit code = %d (err %v), want %d", got, err, tc.wantCode)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
			if n := len(h.backfillRequests(t)); n != 0 {
				t.Errorf("%d backfill requests created for a rejected invocation", n)
			}
		})
	}
}

// The backfillrequest noun works with the read verbs like every other kind.
func TestGetAndDescribeBackfillRequests(t *testing.T) {
	waiting := testBackfillRequest("bfr-1")
	done := testBackfillRequest("bfr-2", decided(v1alpha1.BackfillApproved), func(b *v1alpha1.BackfillRequest) {
		b.Spec.Decision.Reason = "scoped, go ahead"
		b.Status = v1alpha1.BackfillRequestStatus{
			Phase: v1alpha1.BackfillComplete, Attempts: 1, Listed: 12, Ingested: 9,
			Error: "alert 42: repository archived",
		}
	})

	t.Run("get -o name", func(t *testing.T) {
		h := newHarness(t, waiting, done)
		h.opts.Output = "name"
		if err := runGet(t.Context(), h.opts, &getFlags{sortBy: "name"}, "bfr", nil); err != nil {
			t.Fatalf("get bfr: %v", err)
		}
		for _, name := range []string{"bfr-1", "bfr-2"} {
			want := "backfillrequests.patchy.bitwisemedia.uk/" + name
			if !strings.Contains(h.out.String(), want) {
				t.Errorf("stdout = %q, want %q", h.out.String(), want)
			}
		}
	})

	cases := []struct {
		name string
		want []string
	}{
		{
			name: "bfr-1",
			want: []string{"Backfill request bfr-1", "dev@acme.test", "requested", "AwaitingApproval",
				"acme/shop", "awaiting an operator", "patchy approve backfillrequest bfr-1"},
		},
		{
			name: "bfr-2",
			want: []string{"Approved", "lead@acme.test", "scoped, go ahead", "12 listed, 9 ingested",
				"alert 42: repository archived"},
		},
	}
	for _, tc := range cases {
		t.Run("describe "+tc.name, func(t *testing.T) {
			h := newHarness(t, waiting, done)
			h.opts.Output = "markdown"
			if err := runDescribe(t.Context(), h.opts, "backfillrequest", tc.name); err != nil {
				t.Fatalf("describe: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(h.out.String(), want) {
					t.Errorf("describe output missing %q:\n%s", want, h.out.String())
				}
			}
		})
	}
}
