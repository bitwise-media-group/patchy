// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package loadgen

import (
	"reflect"
	"testing"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

func TestFindingDeterministic(t *testing.T) {
	o := Opts{AlertsPerFinding: 3, RepoCardinality: 7, Seed: 42,
		PhaseMix: map[v1alpha1.Phase]int{v1alpha1.PhaseOpened: 1, v1alpha1.PhaseRemediated: 1}}
	for _, i := range []int{0, 1, 999} {
		a, b := Finding(i, o), Finding(i, o)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("Finding(%d) is not deterministic", i)
		}
	}
	if reflect.DeepEqual(Finding(0, o), Finding(1, o)) {
		t.Error("distinct indices produced identical findings")
	}
	if reflect.DeepEqual(Finding(0, o), Finding(0, Opts{Seed: 43})) {
		t.Error("distinct seeds produced identical findings")
	}
}

func TestFindingShape(t *testing.T) {
	o := Opts{AlertsPerFinding: 2, RepoCardinality: 3}
	f := Finding(5, o)
	if f.Labels[v1alpha1.LabelKeyHash] != KeyHash(5, o) {
		t.Errorf("key-hash label = %q, want %q", f.Labels[v1alpha1.LabelKeyHash], KeyHash(5, o))
	}
	if len(f.Labels[v1alpha1.LabelKeyHash]) != 10 {
		t.Errorf("key-hash label length = %d, want 10 (5-byte hex)", len(f.Labels[v1alpha1.LabelKeyHash]))
	}
	if len(f.Spec.Alerts) != 2 {
		t.Errorf("alerts = %d, want 2", len(f.Spec.Alerts))
	}
	if f.Status.Phase != v1alpha1.PhaseOpened {
		t.Errorf("phase = %q, want Opened with no mix", f.Status.Phase)
	}
	if f.Spec.Repository == nil || f.Spec.Repository.URL != RepoURL(5, o) {
		t.Errorf("repository = %+v, want %s", f.Spec.Repository, RepoURL(5, o))
	}
	// Alert cap: the CRD allows at most 64.
	if got := len(Finding(0, Opts{AlertsPerFinding: 100}).Spec.Alerts); got != 64 {
		t.Errorf("alerts under an oversized request = %d, want the 64 cap", got)
	}
}

func TestFindingFamiliesAreDistinct(t *testing.T) {
	o := Opts{RepoCardinality: 2} // families must stay distinct even when repos collide
	seen := map[string]bool{}
	for i := range 100 {
		h := KeyHash(i, o)
		if seen[h] {
			t.Fatalf("key hash %q repeats at index %d", h, i)
		}
		seen[h] = true
	}
}

func TestSourceFindingMatchesFinding(t *testing.T) {
	o := Opts{RepoCardinality: 4}
	// The pairing contract: the scanner-side finding's accumulation inputs
	// (integration, source, repo URL, primary advisory) reproduce the label
	// hash the generated Finding carries.
	for _, i := range []int{0, 3, 41} {
		sf := SourceFinding(i, 0, o)
		fnd := Finding(i, o)
		if sf.Source != fnd.Spec.Source {
			t.Errorf("index %d: source %q != %q", i, sf.Source, fnd.Spec.Source)
		}
		if got := "https://github.com/" + sf.Repo.Owner + "/" + sf.Repo.Name; got != fnd.Spec.Repository.URL {
			t.Errorf("index %d: repo URL %q != %q", i, got, fnd.Spec.Repository.URL)
		}
		if sf.Advisories[0] != fnd.Spec.Advisories[0] {
			t.Errorf("index %d: advisory %q != %q", i, sf.Advisories[0], fnd.Spec.Advisories[0])
		}
	}
}

func TestPhaseMix(t *testing.T) {
	o := Opts{PhaseMix: map[v1alpha1.Phase]int{
		v1alpha1.PhaseEnhanced:   3,
		v1alpha1.PhaseRemediated: 1,
	}}
	counts := map[v1alpha1.Phase]int{}
	for i := range 1000 {
		f := Finding(i, o)
		counts[f.Status.Phase]++
		if v1alpha1.Terminal(f.Status.Phase) != (f.Status.CompletedAt != nil) {
			t.Fatalf("index %d: terminal %v but completedAt %v", i,
				v1alpha1.Terminal(f.Status.Phase), f.Status.CompletedAt)
		}
	}
	if counts[v1alpha1.PhaseEnhanced] == 0 || counts[v1alpha1.PhaseRemediated] == 0 {
		t.Fatalf("phase mix unrepresented: %v", counts)
	}
	if counts[v1alpha1.PhaseEnhanced] <= counts[v1alpha1.PhaseRemediated] {
		t.Errorf("weights ignored: %v", counts)
	}
	if counts[v1alpha1.PhaseOpened] != 0 {
		t.Errorf("unweighted phase appeared: %v", counts)
	}
}

func TestRollups(t *testing.T) {
	rs := Rollups(3)
	if len(rs) != 3 {
		t.Fatalf("rollups = %d, want 3", len(rs))
	}
	if rs[0].Spec.Scope.Type != v1alpha1.ScopeTotal || rs[0].Name != "total" {
		t.Errorf("rollup 0 = %s/%s, want the total scope", rs[0].Name, rs[0].Spec.Scope.Type)
	}
	for _, fr := range rs[1:] {
		if fr.Spec.Scope.Type != v1alpha1.ScopeRepository {
			t.Errorf("rollup %s scope = %s, want repository", fr.Name, fr.Spec.Scope.Type)
		}
	}
	if got := len(rs[0].Status.Recent); got != 512 {
		t.Errorf("ledger = %d entries, want the full 512", got)
	}
	if got := len(rs[0].Status.Monthly); got != 24 {
		t.Errorf("monthly = %d entries, want 24", got)
	}
}

func TestInvestigations(t *testing.T) {
	o := Opts{}
	invs := Investigations(2, o)
	if len(invs) != 2 {
		t.Fatalf("investigations = %d, want 2", len(invs))
	}
	for i, inv := range invs {
		if want := FindingName(i, o); inv.Labels[v1alpha1.LabelFinding] != want {
			t.Errorf("investigation %d finding label = %q, want %q", i, inv.Labels[v1alpha1.LabelFinding], want)
		}
		if inv.Status.Stage == nil || inv.Status.Stage.Outcome != "ok" {
			t.Errorf("investigation %d stage = %+v, want a completed ok stage", i, inv.Status.Stage)
		}
	}
}
