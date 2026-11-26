// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package render_test

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/render"
)

func testBackfillRequest(mutate func(*v1alpha1.BackfillRequest)) *v1alpha1.BackfillRequest {
	b := &v1alpha1.BackfillRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "backfill-x7k2p",
			CreationTimestamp: metav1.NewTime(testClock.Add(-time.Hour)),
		},
		Spec: v1alpha1.BackfillRequestSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			RequestedBy:    "dev@acme.test",
		},
	}
	if mutate != nil {
		mutate(b)
	}
	return b
}

func TestBackfillRequestDetail(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*v1alpha1.BackfillRequest)
		want    []string
		exclude []string
	}{
		{
			name: "a granted request has no decision section",
			mutate: func(b *v1alpha1.BackfillRequest) {
				b.Status.Phase = v1alpha1.BackfillPending
			},
			want: []string{
				"Backfill request backfill-x7k2p", "gh", "dev@acme.test", "backfill", "Pending", "full scope", "1h0m",
			},
			exclude: []string{"Decision", "Outcome"},
		},
		{
			name: "a waiting request says how to decide it",
			mutate: func(b *v1alpha1.BackfillRequest) {
				b.Spec.Via = v1alpha1.BackfillViaRequested
				b.Spec.Repositories = []string{"acme/", "beta/shop"}
				b.Status.Phase = v1alpha1.BackfillAwaitingApproval
			},
			want: []string{"requested", "acme/, beta/shop", "awaiting an operator",
				"patchy approve backfillrequest backfill-x7k2p", "patchy deny backfillrequest backfill-x7k2p"},
		},
		{
			name: "a decided, finished request shows the verdict and the outcome",
			mutate: func(b *v1alpha1.BackfillRequest) {
				b.Spec.Via = v1alpha1.BackfillViaRequested
				b.Spec.Decision = &v1alpha1.BackfillDecision{
					Verdict: v1alpha1.BackfillApproved, By: "lead@acme.test",
					At: metav1.NewTime(testClock.Add(-30 * time.Minute)), Reason: "go ahead",
				}
				b.Status = v1alpha1.BackfillRequestStatus{
					Phase: v1alpha1.BackfillComplete, Attempts: 2, Listed: 40, Ingested: 31,
					Truncated: true, Error: "alert 7: boom",
				}
			},
			want: []string{"Approved", "lead@acme.test", "go ahead", "40 listed, 31 ingested (truncated)",
				"narrower --repo", "alert 7: boom"},
			exclude: []string{"awaiting an operator"},
		},
		{
			name: "a maintained request names the GitHub identity",
			mutate: func(b *v1alpha1.BackfillRequest) {
				b.Spec.Via = v1alpha1.BackfillViaMaintained
				b.Spec.GitHub = &v1alpha1.BackfillGitHubIdentity{Login: "octocat", Role: "maintain"}
			},
			want: []string{"maintained", "octocat (maintain)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := testBackfillRequest(tc.mutate)
			got := doc(func(d *printer.Doc) { render.BackfillRequestDetail(d, b, testClock) })
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("detail missing %q:\n%s", want, got)
				}
			}
			for _, exclude := range tc.exclude {
				if strings.Contains(got, exclude) {
					t.Errorf("detail contains %q:\n%s", exclude, got)
				}
			}
		})
	}
}
