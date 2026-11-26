// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/action"
)

// testBackfillRequest builds a request awaiting an operator, as
// `patchy backfill --request` files it.
func testBackfillRequest(name string, mutate ...func(*v1alpha1.BackfillRequest)) *v1alpha1.BackfillRequest {
	b := &v1alpha1.BackfillRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         testNamespace,
			CreationTimestamp: metav1.NewTime(testClock.Add(-30 * time.Minute)),
			Labels:            map[string]string{v1alpha1.LabelIntegration: "gh"},
		},
		Spec: v1alpha1.BackfillRequestSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			RequestedBy:    "dev@acme.test",
			Repositories:   []string{"acme/shop"},
			Via:            v1alpha1.BackfillViaRequested,
		},
		Status: v1alpha1.BackfillRequestStatus{Phase: v1alpha1.BackfillAwaitingApproval},
	}
	for _, m := range mutate {
		m(b)
	}
	return b
}

// decided returns a mutator that records an earlier operator's verdict.
func decided(verdict v1alpha1.BackfillVerdict) func(*v1alpha1.BackfillRequest) {
	return func(b *v1alpha1.BackfillRequest) {
		b.Spec.Decision = &v1alpha1.BackfillDecision{
			Verdict: verdict,
			By:      "lead@acme.test",
			At:      metav1.NewTime(testClock.Add(-time.Minute)),
		}
	}
}

// backfillRequest returns the named request's current state.
func (h *harness) backfillRequest(t *testing.T, name string) *v1alpha1.BackfillRequest {
	t.Helper()
	var b v1alpha1.BackfillRequest
	key := types.NamespacedName{Namespace: testNamespace, Name: name}
	if err := h.client.Get(context.Background(), key, &b); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return &b
}

func TestDecisionApplies(t *testing.T) {
	cases := []struct {
		name         string
		req          *v1alpha1.BackfillRequest
		verdict      v1alpha1.BackfillVerdict
		flags        *decisionFlags
		wantCode     int
		wantOut      string
		wantErr      string
		wantDecision *v1alpha1.BackfillDecision
	}{
		{
			name:     "approve a waiting request",
			req:      testBackfillRequest("bfr-1"),
			verdict:  v1alpha1.BackfillApproved,
			flags:    &decisionFlags{},
			wantCode: ExitOK,
			wantOut:  "bfr-1 approved",
			wantDecision: &v1alpha1.BackfillDecision{
				Verdict: v1alpha1.BackfillApproved,
				By:      "op@acme.test",
				At:      metav1.NewTime(testClock),
			},
		},
		{
			name:     "deny records the reason",
			req:      testBackfillRequest("bfr-1"),
			verdict:  v1alpha1.BackfillDeniedVerdict,
			flags:    &decisionFlags{reason: "too broad"},
			wantCode: ExitOK,
			wantOut:  "bfr-1 denied",
			wantDecision: &v1alpha1.BackfillDecision{
				Verdict: v1alpha1.BackfillDeniedVerdict,
				By:      "op@acme.test",
				At:      metav1.NewTime(testClock),
				Reason:  "too broad",
			},
		},
		{
			name:     "repeating the same verdict is a no-op success",
			req:      testBackfillRequest("bfr-1", decided(v1alpha1.BackfillApproved)),
			verdict:  v1alpha1.BackfillApproved,
			flags:    &decisionFlags{},
			wantCode: ExitOK,
			wantOut:  "bfr-1 was already approved",
			wantDecision: &v1alpha1.BackfillDecision{
				Verdict: v1alpha1.BackfillApproved,
				By:      "lead@acme.test",
				At:      metav1.NewTime(testClock.Add(-time.Minute)),
			},
		},
		{
			name:     "a decision is final",
			req:      testBackfillRequest("bfr-1", decided(v1alpha1.BackfillDeniedVerdict)),
			verdict:  v1alpha1.BackfillApproved,
			flags:    &decisionFlags{},
			wantCode: ExitDenied,
			wantErr:  "already denied by lead@acme.test",
			wantDecision: &v1alpha1.BackfillDecision{
				Verdict: v1alpha1.BackfillDeniedVerdict,
				By:      "lead@acme.test",
				At:      metav1.NewTime(testClock.Add(-time.Minute)),
			},
		},
		{
			name: "a request that runs at once takes no decision",
			req: testBackfillRequest("bfr-1", func(b *v1alpha1.BackfillRequest) {
				b.Spec.Via = ""
				b.Status.Phase = v1alpha1.BackfillPending
			}),
			verdict:  v1alpha1.BackfillApproved,
			flags:    &decisionFlags{},
			wantCode: ExitDenied,
			wantErr:  `filed via "backfill"`,
		},
		{
			name: "a maintained request takes no decision",
			req: testBackfillRequest("bfr-1", func(b *v1alpha1.BackfillRequest) {
				b.Spec.Via = v1alpha1.BackfillViaMaintained
			}),
			verdict:  v1alpha1.BackfillDeniedVerdict,
			flags:    &decisionFlags{},
			wantCode: ExitDenied,
			wantErr:  `filed via "maintained"`,
		},
		{
			name:     "dry run writes nothing",
			req:      testBackfillRequest("bfr-1"),
			verdict:  v1alpha1.BackfillApproved,
			flags:    &decisionFlags{dryRun: true},
			wantCode: ExitOK,
			wantOut:  "bfr-1 would be approved",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.req)
			err := runDecision(t.Context(), h.opts, tc.flags, tc.verdict, []string{tc.req.Name})
			if got := exitCode(err); got != tc.wantCode {
				t.Fatalf("exit code = %d (err %v), want %d", got, err, tc.wantCode)
			}
			if tc.wantErr != "" && !strings.Contains(h.errOut.String(), tc.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", h.errOut.String(), tc.wantErr)
			}
			if tc.wantOut != "" && !strings.Contains(h.out.String(), tc.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", h.out.String(), tc.wantOut)
			}
			got := h.backfillRequest(t, tc.req.Name).Spec.Decision
			if !decisionEqual(got, tc.wantDecision) {
				t.Errorf("decision = %+v, want %+v", got, tc.wantDecision)
			}
		})
	}
}

// decisionEqual compares decisions with At at the second precision the API
// stores.
func decisionEqual(a, b *v1alpha1.BackfillDecision) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Verdict == b.Verdict && a.By == b.By && a.Reason == b.Reason &&
		a.At.Unix() == b.At.Unix()
}

// The access review gates the write, and it asks about the backfill verb on
// integrations: the operator's grant, not approve on findings.
func TestDecisionDeniedStopsBeforeWriting(t *testing.T) {
	h := newHarness(t, testBackfillRequest("bfr-1"))
	asked := h.recordAccess(func(string, string) bool { return false })

	err := runDecision(t.Context(), h.opts, &decisionFlags{}, v1alpha1.BackfillApproved, []string{"bfr-1"})
	if !errors.Is(err, errDenied) {
		t.Fatalf("err = %v, want errDenied", err)
	}
	if !strings.Contains(err.Error(), "'backfill' verb on integrations") {
		t.Errorf("error = %q, want it to name the missing verb", err)
	}
	if want := [][2]string{{"integrations", action.VerbBackfill}}; !reflect.DeepEqual(*asked, want) {
		t.Errorf("access reviews = %v, want %v", *asked, want)
	}
	if h.backfillRequest(t, "bfr-1").Spec.Decision != nil {
		t.Error("a denied decision still wrote to the request")
	}
}

// One undecidable request must not stop the rest, and the run still fails.
func TestDecisionBatchReportsEveryTarget(t *testing.T) {
	h := newHarness(t,
		testBackfillRequest("bfr-1"),
		testBackfillRequest("bfr-2", func(b *v1alpha1.BackfillRequest) { b.Spec.Via = "" }),
		testBackfillRequest("bfr-3"),
	)
	err := runDecision(t.Context(), h.opts, &decisionFlags{}, v1alpha1.BackfillDeniedVerdict,
		[]string{"bfr-1", "bfr-2", "bfr-3", "bfr-missing"})
	if err == nil {
		t.Fatal("batch succeeded despite failures")
	}
	for _, name := range []string{"bfr-1", "bfr-3"} {
		if d := h.backfillRequest(t, name).Spec.Decision; d == nil || d.Verdict != v1alpha1.BackfillDeniedVerdict {
			t.Errorf("%s decision = %+v, want denied", name, d)
		}
	}
	for _, name := range []string{"bfr-2", "bfr-missing"} {
		if !strings.Contains(h.errOut.String(), name) {
			t.Errorf("stderr = %q, want %s named", h.errOut.String(), name)
		}
	}
}

func TestDecisionRejectsBadInput(t *testing.T) {
	h := newHarness(t, testBackfillRequest("bfr-1"))
	cases := []struct {
		name  string
		flags *decisionFlags
		names []string
	}{
		{name: "no names", flags: &decisionFlags{}},
		{
			name:  "an over-long reason",
			flags: &decisionFlags{reason: strings.Repeat("x", maxDecisionReason+1)},
			names: []string{"bfr-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runDecision(t.Context(), h.opts, tc.flags, v1alpha1.BackfillDeniedVerdict, tc.names)
			if got := exitCode(err); got != ExitUsage {
				t.Errorf("exit code = %d (err %v), want %d", got, err, ExitUsage)
			}
		})
	}
	if h.backfillRequest(t, "bfr-1").Spec.Decision != nil {
		t.Error("a rejected invocation wrote a decision")
	}
}

// TestDecisionCommands drives approve and deny through cobra, which is where
// the noun dispatch and the --note/--reason translation live.
func TestDecisionCommands(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		wantCode     int
		wantVerdict  v1alpha1.BackfillVerdict
		wantReason   string
		wantErr      string
		wantNoChange bool
	}{
		{
			name:        "approve with an explicit note records it as the reason",
			args:        []string{"approve", "backfillrequest", "bfr-1", "--note", "scoped, go ahead"},
			wantVerdict: v1alpha1.BackfillApproved,
			wantReason:  "scoped, go ahead",
		},
		{
			// The default note is written for findings, so it stays off the
			// decision.
			name:        "approve without a note leaves the reason empty",
			args:        []string{"approve", "bfr", "bfr-1"},
			wantVerdict: v1alpha1.BackfillApproved,
		},
		{
			name:        "deny with a reason",
			args:        []string{"deny", "backfillrequests", "bfr-1", "--reason", "too broad"},
			wantVerdict: v1alpha1.BackfillDeniedVerdict,
			wantReason:  "too broad",
		},
		{
			name:         "approve refuses a selector for backfill requests",
			args:         []string{"approve", "bfr", "-l", "x=y"},
			wantCode:     ExitUsage,
			wantErr:      "-l is not supported",
			wantNoChange: true,
		},
		{
			name:         "deny applies only to backfill requests",
			args:         []string{"deny", "finding", "fnd-1"},
			wantCode:     ExitUsage,
			wantErr:      "deny applies to backfill requests",
			wantNoChange: true,
		},
		{
			name:         "approve names both nouns when given another",
			args:         []string{"approve", "investigation", "x"},
			wantCode:     ExitUsage,
			wantErr:      "findings and backfill requests",
			wantNoChange: true,
		},
		{
			name:         "suspend still applies only to findings",
			args:         []string{"suspend", "bfr", "bfr-1"},
			wantCode:     ExitUsage,
			wantErr:      "suspend applies to findings",
			wantNoChange: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testBackfillRequest("bfr-1"))
			root := NewRoot(h.opts)
			root.SetArgs(tc.args)
			root.SetOut(h.out)
			root.SetErr(h.errOut)
			err := root.ExecuteContext(t.Context())
			if got := exitCode(err); got != tc.wantCode {
				t.Fatalf("exit code = %d (err %v), want %d", got, err, tc.wantCode)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
			d := h.backfillRequest(t, "bfr-1").Spec.Decision
			if tc.wantNoChange {
				if d != nil {
					t.Errorf("decision = %+v, want none", d)
				}
				return
			}
			if d == nil || d.Verdict != tc.wantVerdict || d.Reason != tc.wantReason || d.By != "op@acme.test" {
				t.Errorf("decision = %+v, want %s by op@acme.test with reason %q", d, tc.wantVerdict, tc.wantReason)
			}
		})
	}
}

// approve keeps working on findings alongside the new noun.
func TestApproveStillAppliesToFindings(t *testing.T) {
	h := newHarness(t, testFinding("fnd-1", v1alpha1.PhaseAwaitingApproval))
	root := NewRoot(h.opts)
	root.SetArgs([]string{"approve", "finding", "fnd-1"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("approve finding: %v", err)
	}
	a := h.finding(t, "fnd-1").Spec.Approval
	if a == nil || a.By != "op@acme.test" || a.Note != cliNote {
		t.Errorf("approval = %+v, want one by op@acme.test with the CLI note", a)
	}
}
