// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
)

func d(id int64, guid string, ok, redelivery bool) ghclient.Delivery {
	return ghclient.Delivery{ID: id, GUID: guid, OK: ok, Redelivery: redelivery}
}

func TestPickRedeliveries(t *testing.T) {
	tests := []struct {
		name string
		// newest-first, as the log walk yields them
		window []ghclient.Delivery
		want   []int64
	}{
		{"empty", nil, nil},
		{"all ok", []ghclient.Delivery{d(1, "a", true, false), d(2, "b", true, false)}, nil},
		{"one failed", []ghclient.Delivery{d(1, "a", true, false), d(2, "b", false, false)}, []int64{2}},
		{
			"failure later redelivered ok",
			[]ghclient.Delivery{d(3, "a", true, true), d(2, "a", false, false)},
			nil,
		},
		{
			"failed redelivery retries the newest attempt",
			[]ghclient.Delivery{d(3, "a", false, true), d(2, "a", false, false)},
			[]int64{3},
		},
		{
			"attempt cap gives up",
			[]ghclient.Delivery{d(4, "a", false, true), d(3, "a", false, true), d(2, "a", false, false)},
			nil,
		},
		{
			"mixed guids stay independent",
			[]ghclient.Delivery{d(5, "b", false, false), d(4, "a", true, true), d(2, "a", false, false)},
			[]int64{5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickRedeliveries(tt.window, maxRedeliveryAttempts); !slices.Equal(got, tt.want) {
				t.Errorf("pickRedeliveries() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPickReplays(t *testing.T) {
	window := []ghclient.Delivery{
		d(5, "b", false, false),
		d(4, "a", true, true),
		d(2, "a", false, false),
		d(1, "c", true, false),
	}
	// One per GUID, the newest attempt, success included.
	if got, want := pickReplays(window), []int64{5, 4, 1}; !slices.Equal(got, want) {
		t.Errorf("pickReplays() = %v, want %v", got, want)
	}
}

func TestPendingReplay(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	earlier := metav1.NewTime(at.Add(-time.Hour))
	tests := []struct {
		name    string
		integ   v1alpha1.Integration
		pending bool
	}{
		{"no request", v1alpha1.Integration{}, false},
		{
			"unhandled request",
			v1alpha1.Integration{Spec: v1alpha1.IntegrationSpec{Replay: &v1alpha1.ActionRequest{At: at}}},
			true,
		},
		{
			"handled request",
			v1alpha1.Integration{
				Spec:   v1alpha1.IntegrationSpec{Replay: &v1alpha1.ActionRequest{At: at}},
				Status: v1alpha1.IntegrationStatus{Redelivery: &v1alpha1.RedeliveryStatus{ReplayedAt: &at}},
			},
			false,
		},
		{
			"newer request supersedes the handled one",
			v1alpha1.Integration{
				Spec:   v1alpha1.IntegrationSpec{Replay: &v1alpha1.ActionRequest{At: at}},
				Status: v1alpha1.IntegrationStatus{Redelivery: &v1alpha1.RedeliveryStatus{ReplayedAt: &earlier}},
			},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pendingReplay(&tt.integ) != nil; got != tt.pending {
				t.Errorf("pendingReplay() != nil = %v, want %v", got, tt.pending)
			}
		})
	}
}

func TestPendingReset(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	earlier := metav1.NewTime(at.Add(-time.Hour))
	tests := []struct {
		name    string
		integ   v1alpha1.Integration
		pending bool
	}{
		{"no request", v1alpha1.Integration{}, false},
		{
			"unhandled request",
			v1alpha1.Integration{Spec: v1alpha1.IntegrationSpec{Reset: &v1alpha1.ActionRequest{At: at}}},
			true,
		},
		{
			"handled request",
			v1alpha1.Integration{
				Spec:   v1alpha1.IntegrationSpec{Reset: &v1alpha1.ActionRequest{At: at}},
				Status: v1alpha1.IntegrationStatus{ResetAt: &at},
			},
			false,
		},
		{
			"newer request supersedes the handled one",
			v1alpha1.Integration{
				Spec:   v1alpha1.IntegrationSpec{Reset: &v1alpha1.ActionRequest{At: at}},
				Status: v1alpha1.IntegrationStatus{ResetAt: &earlier},
			},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pendingReset(&tt.integ) != nil; got != tt.pending {
				t.Errorf("pendingReset() != nil = %v, want %v", got, tt.pending)
			}
		})
	}
}
