// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/resource"
	"github.com/bitwise-media-group/patchy/internal/action"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

// testClock pins the CLI's clock so age and timestamp output is stable.
var testClock = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

const testNamespace = "patchy"

// harness is one CLI invocation against a fake cluster.
type harness struct {
	opts   *Options
	client client.Client
	out    *bytes.Buffer
	errOut *bytes.Buffer
}

// newHarness wires opts to a fake client holding objs, granting every verb
// unless the test narrows it with deny.
func newHarness(t *testing.T, objs ...client.Object) *harness {
	t.Helper()
	now = func() time.Time { return testClock }
	t.Cleanup(func() { now = time.Now })

	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}).
		Build()

	h := &harness{
		client: c,
		out:    &bytes.Buffer{},
		errOut: &bytes.Buffer{},
	}
	h.opts = &Options{
		Out:            h.out,
		ErrOut:         h.errOut,
		Output:         "table",
		NoColor:        true,
		RequestTimeout: 5 * time.Second,
	}
	h.opts.WithEnv(&kubecfg.Env{Client: c, Namespace: testNamespace})
	h.opts.WithAccess(func(context.Context, *kubecfg.Env, string) (bool, error) { return true, nil })
	h.opts.WithIdentity(func() string { return "op@acme.test" })
	return h
}

// deny makes every access review answer no.
func (h *harness) deny() {
	h.opts.WithAccess(func(context.Context, *kubecfg.Env, string) (bool, error) { return false, nil })
}

// finding returns the named finding's current state.
func (h *harness) finding(t *testing.T, name string) *v1alpha1.Finding {
	t.Helper()
	var f v1alpha1.Finding
	key := types.NamespacedName{Namespace: testNamespace, Name: name}
	if err := h.client.Get(context.Background(), key, &f); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return &f
}

// testFinding builds a finding in the given phase.
func testFinding(name string, phase v1alpha1.Phase, mutate ...func(*v1alpha1.Finding)) *v1alpha1.Finding {
	f := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         testNamespace,
			CreationTimestamp: metav1.NewTime(testClock.Add(-2 * time.Hour)),
			Labels:            map[string]string{v1alpha1.LabelSeverity: "high"},
		},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "github-code-scanning",
			Advisories:     []string{"CVE-2026-0001"},
			Title:          "Command injection in the build script",
			Severity:       v1alpha1.LevelHigh,
		},
		Status: v1alpha1.FindingStatus{Phase: phase},
	}
	for _, m := range mutate {
		m(f)
	}
	return f
}

func TestActionApplies(t *testing.T) {
	cases := []struct {
		name        string
		finding     *v1alpha1.Finding
		verb        string
		wantErr     bool
		wantOut     string
		wantApplied func(t *testing.T, f *v1alpha1.Finding)
	}{
		{
			name:    "suspend a queued finding",
			finding: testFinding("fnd-1", v1alpha1.PhaseQueued),
			verb:    action.VerbSuspend,
			wantOut: "fnd-1 suspended",
			wantApplied: func(t *testing.T, f *v1alpha1.Finding) {
				if !f.Spec.Suspend {
					t.Error("spec.suspend not written")
				}
			},
		},
		{
			name:    "approve records the caller",
			finding: testFinding("fnd-1", v1alpha1.PhaseAwaitingApproval),
			verb:    action.VerbApprove,
			wantOut: "fnd-1 approved",
			wantApplied: func(t *testing.T, f *v1alpha1.Finding) {
				if f.Spec.Approval == nil {
					t.Fatal("spec.approval not written")
				}
				if f.Spec.Approval.By != "op@acme.test" {
					t.Errorf("approval.by = %q, want the reviewed identity", f.Spec.Approval.By)
				}
			},
		},
		{
			name: "repeating an action is a no-op success",
			finding: testFinding("fnd-1", v1alpha1.PhaseQueued, func(f *v1alpha1.Finding) {
				f.Spec.Suspend = true
			}),
			verb:    action.VerbSuspend,
			wantOut: "fnd-1 was already suspended",
		},
		{
			name:    "an action the phase has no use for fails",
			finding: testFinding("fnd-1", v1alpha1.PhaseRemediated),
			verb:    action.VerbSuspend,
			wantErr: true,
			wantApplied: func(t *testing.T, f *v1alpha1.Finding) {
				if f.Spec.Suspend {
					t.Error("suspend written despite being unavailable")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.finding)
			err := runAction(t.Context(), h.opts, &actionFlags{}, tc.verb, "finding", []string{tc.finding.Name})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantOut != "" && !strings.Contains(h.out.String(), tc.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", h.out.String(), tc.wantOut)
			}
			if tc.wantApplied != nil {
				tc.wantApplied(t, h.finding(t, tc.finding.Name))
			}
		})
	}
}

// TestActionDryRunWritesNothing is the guarantee --dry-run exists for.
func TestActionDryRunWritesNothing(t *testing.T) {
	h := newHarness(t, testFinding("fnd-1", v1alpha1.PhaseQueued))
	err := runAction(t.Context(), h.opts, &actionFlags{dryRun: true},
		action.VerbSuspend, "finding", []string{"fnd-1"})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if got := h.out.String(); !strings.Contains(got, "would be") {
		t.Errorf("stdout = %q, want it to say what would happen", got)
	}
	if h.finding(t, "fnd-1").Spec.Suspend {
		t.Error("dry run wrote spec.suspend")
	}
}

// TestActionDeniedStopsBeforeWriting proves the access review gates the write
// rather than merely decorating the error afterwards.
func TestActionDeniedStopsBeforeWriting(t *testing.T) {
	h := newHarness(t, testFinding("fnd-1", v1alpha1.PhaseQueued))
	h.deny()

	err := runAction(t.Context(), h.opts, &actionFlags{}, action.VerbSuspend, "finding", []string{"fnd-1"})
	if err == nil {
		t.Fatal("action succeeded despite the access review saying no")
	}
	if got := exitCode(err); got != ExitDenied {
		t.Errorf("exit code = %d, want %d", got, ExitDenied)
	}
	if h.finding(t, "fnd-1").Spec.Suspend {
		t.Error("a denied action still wrote to the finding")
	}
}

// TestActionBulkReportsEveryTarget covers the selector path: one unavailable
// finding must not stop the others, and the run must still fail overall.
func TestActionBulkReportsEveryTarget(t *testing.T) {
	h := newHarness(t,
		testFinding("fnd-1", v1alpha1.PhaseQueued),
		testFinding("fnd-2", v1alpha1.PhaseRemediated), // terminal: cannot suspend
		testFinding("fnd-3", v1alpha1.PhaseOpened),
	)
	f := &actionFlags{selector: v1alpha1.LabelSeverity + "=high", yes: true}
	err := runAction(t.Context(), h.opts, f, action.VerbSuspend, "finding", nil)
	if err == nil {
		t.Fatal("bulk action succeeded despite one failure")
	}
	for _, name := range []string{"fnd-1", "fnd-3"} {
		if !h.finding(t, name).Spec.Suspend {
			t.Errorf("%s was not suspended; one failure stopped the batch", name)
		}
	}
	if !strings.Contains(h.errOut.String(), "fnd-2") {
		t.Errorf("stderr = %q, want the failing finding named", h.errOut.String())
	}
}

func TestActionRejectsWrongNoun(t *testing.T) {
	h := newHarness(t)
	err := runAction(t.Context(), h.opts, &actionFlags{}, action.VerbSuspend, "investigation", []string{"x"})
	if got := exitCode(err); got != ExitUsage {
		t.Errorf("exit code = %d, want %d for a non-finding noun", got, ExitUsage)
	}
}

// TestActionRefusesAllNamespaces guards against a bulk write escaping its
// namespace: an -A selector could otherwise touch the whole cluster.
func TestActionRefusesAllNamespaces(t *testing.T) {
	h := newHarness(t, testFinding("fnd-1", v1alpha1.PhaseQueued))
	h.opts.WithEnv(&kubecfg.Env{Client: h.client, Namespace: ""})

	err := runAction(t.Context(), h.opts, &actionFlags{selector: "x=y"},
		action.VerbSuspend, "finding", nil)
	if got := exitCode(err); got != ExitUsage {
		t.Errorf("exit code = %d, want %d", got, ExitUsage)
	}
}

func TestGetObjectsFiltersAndFormats(t *testing.T) {
	cases := []struct {
		name    string
		format  string
		flags   *getFlags
		want    []string
		exclude []string
	}{
		{
			name:    "phase filter",
			format:  "name",
			flags:   &getFlags{phase: []string{"AwaitingApproval"}},
			want:    []string{"fnd-1"},
			exclude: []string{"fnd-2", "fnd-3"},
		},
		{
			name:    "phase filter is case-insensitive",
			format:  "name",
			flags:   &getFlags{phase: []string{"awaitingapproval"}},
			want:    []string{"fnd-1"},
			exclude: []string{"fnd-2"},
		},
		{
			name:    "suspended filter",
			format:  "name",
			flags:   &getFlags{suspended: true},
			want:    []string{"fnd-3"},
			exclude: []string{"fnd-1", "fnd-2"},
		},
		{
			name:   "awaiting filter drops terminal findings",
			format: "name",
			flags:  &getFlags{awaiting: true},
			// fnd-2 is Remediated, so no action applies to it.
			want:    []string{"fnd-1", "fnd-3"},
			exclude: []string{"fnd-2"},
		},
		{
			name:    "name format is fully qualified",
			format:  "name",
			flags:   &getFlags{phase: []string{"Queued"}},
			want:    []string{"findings.patchy.bitwisemedia.uk/fnd-3"},
			exclude: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t,
				testFinding("fnd-1", v1alpha1.PhaseAwaitingApproval),
				testFinding("fnd-2", v1alpha1.PhaseRemediated),
				testFinding("fnd-3", v1alpha1.PhaseQueued, func(f *v1alpha1.Finding) {
					f.Spec.Suspend = true
				}),
			)
			h.opts.Output = tc.format
			if tc.flags.sortBy == "" {
				tc.flags.sortBy = "name"
			}
			if err := runGet(t.Context(), h.opts, tc.flags, "findings", nil); err != nil {
				t.Fatalf("get: %v", err)
			}
			got := h.out.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("stdout = %q, want it to contain %q", got, want)
				}
			}
			for _, exclude := range tc.exclude {
				if strings.Contains(got, exclude) {
					t.Errorf("stdout = %q, want it to exclude %q", got, exclude)
				}
			}
		})
	}
}

// TestGetRejectsFindingFiltersOnOtherNouns: silently ignoring a filter would
// show the user a list they believe is filtered.
func TestGetRejectsFindingFiltersOnOtherNouns(t *testing.T) {
	h := newHarness(t)
	err := runGet(t.Context(), h.opts, &getFlags{phase: []string{"Queued"}}, "investigations", nil)
	if got := exitCode(err); got != ExitUsage {
		t.Fatalf("exit code = %d, want %d", got, ExitUsage)
	}
	if !strings.Contains(err.Error(), "--phase") {
		t.Errorf("error = %q, want it to name the offending flag", err)
	}
}

func TestGetUnknownNoun(t *testing.T) {
	h := newHarness(t)
	err := runGet(t.Context(), h.opts, &getFlags{}, "widgets", nil)
	if got := exitCode(err); got != ExitUsage {
		t.Fatalf("exit code = %d, want %d", got, ExitUsage)
	}
}

func TestResolveRunName(t *testing.T) {
	fnd := testFinding("fnd-1", v1alpha1.PhaseInReview, func(f *v1alpha1.Finding) {
		f.Status.Investigation = &v1alpha1.InvestigationSummary{Name: "fnd-1-inv-2", Attempt: 2}
	})
	h := newHarness(t, fnd)
	env, err := h.opts.Connect()
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	inv := lookupKind(t, "investigation")

	cases := []struct {
		name  string
		flags *reviewFlags
		arg   string
		want  string
	}{
		{
			name:  "explicit name wins",
			flags: &reviewFlags{},
			arg:   "fnd-1-inv-1",
			want:  "fnd-1-inv-1",
		},
		{
			// The controllers mint <finding>-inv-<n>, so an explicit attempt
			// needs no lookup at all.
			name:  "attempt is derived, not looked up",
			flags: &reviewFlags{finding: "fnd-1", attempt: 1},
			want:  "fnd-1-inv-1",
		},
		{
			name:  "latest comes from the finding's status",
			flags: &reviewFlags{finding: "fnd-1"},
			want:  "fnd-1-inv-2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRunName(t.Context(), env, inv, tc.flags, tc.arg)
			if err != nil {
				t.Fatalf("resolveRunName: %v", err)
			}
			if got != tc.want {
				t.Errorf("name = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveRunNameWithoutRun(t *testing.T) {
	h := newHarness(t, testFinding("fnd-1", v1alpha1.PhaseOpened))
	env, err := h.opts.Connect()
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	inv := lookupKind(t, "investigation")
	if _, err := resolveRunName(t.Context(), env, inv, &reviewFlags{finding: "fnd-1"}, ""); err == nil {
		t.Fatal("resolved a run for a finding that has not been investigated")
	}
}

func TestBuildSelector(t *testing.T) {
	cases := []struct {
		name    string
		flags   *getFlags
		want    string
		wantErr bool
	}{
		{name: "empty", flags: &getFlags{}, want: ""},
		{
			name:  "severity becomes a set expression",
			flags: &getFlags{severity: []string{"high", "critical"}},
			want:  "patchy.bitwisemedia.uk/severity in (high,critical)",
		},
		{
			name:  "finding narrows children server-side",
			flags: &getFlags{finding: "fnd-1"},
			want:  "patchy.bitwisemedia.uk/finding=fnd-1",
		},
		{
			name:  "explicit selector is preserved alongside",
			flags: &getFlags{selector: "team=platform", source: "github-code-scanning"},
			want:  "team=platform,patchy.bitwisemedia.uk/source=github-code-scanning",
		},
		{
			name:    "an unknown severity is a usage error, not a silent empty result",
			flags:   &getFlags{severity: []string{"catastrophic"}},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildSelector(tc.flags)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("selector = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPast(t *testing.T) {
	// The result lines read "fnd-1 <past>", so the verb has to inflect.
	for verb, want := range map[string]string{
		action.VerbApprove:  "approved",
		action.VerbRetry:    "retried",
		action.VerbResume:   "resumed",
		action.VerbSuspend:  "suspended",
		action.VerbExpedite: "expedited",
	} {
		if got := past(verb); got != want {
			t.Errorf("past(%q) = %q, want %q", verb, got, want)
		}
	}
}

// lookupKind resolves a noun for tests that need a resource.Kind.
func lookupKind(t *testing.T, noun string) resource.Kind {
	t.Helper()
	k, err := resource.Lookup(noun)
	if err != nil {
		t.Fatalf("lookup %s: %v", noun, err)
	}
	return k
}
