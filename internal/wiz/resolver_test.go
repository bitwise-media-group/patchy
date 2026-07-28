// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package wiz

import (
	"context"
	"errors"
	"testing"

	"github.com/bitwise-media-group/patchy/pkg/source"
)

// fakeRejecter records rejections and can fail selected issues.
type fakeRejecter struct {
	rejected []struct{ id, reason, note string }
	fail     map[string]error
}

func (f *fakeRejecter) RejectIssue(_ context.Context, id, reason, note string) error {
	if err := f.fail[id]; err != nil {
		return err
	}
	f.rejected = append(f.rejected, struct{ id, reason, note string }{id, reason, note})
	return nil
}

func TestResolverRejectsEveryAlert(t *testing.T) {
	api := &fakeRejecter{}
	r := NewResolver(api)
	alerts := []source.AlertRef{
		{ID: "iss-1", Source: IssuesID},
		{ID: "iss-2", Source: IssuesID},
		{ID: "", Source: IssuesID}, // an alert with no id has nothing to reject
	}
	v := source.Verdict{Kind: source.VerdictIgnore, Reason: "false positive", Comment: "not exploitable"}
	if err := r.Resolve(t.Context(), alerts, v); err != nil {
		t.Fatalf("Resolve() = %v, want nil", err)
	}
	if len(api.rejected) != 2 {
		t.Fatalf("rejected %d issues, want 2", len(api.rejected))
	}
	if api.rejected[0].reason != "FALSE_POSITIVE" || api.rejected[0].note != "not exploitable" {
		t.Errorf("rejection = %+v, want FALSE_POSITIVE with the comment", api.rejected[0])
	}
}

func TestResolverReasonMapping(t *testing.T) {
	api := &fakeRejecter{}
	v := source.Verdict{Kind: source.VerdictIgnore, Reason: "accepted risk"}
	if err := NewResolver(api).Resolve(t.Context(), []source.AlertRef{{ID: "iss-1"}}, v); err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if api.rejected[0].reason != "WONT_FIX" {
		t.Errorf("reason = %q, want WONT_FIX for a non-false-positive ignore", api.rejected[0].reason)
	}
}

func TestResolverIgnoresOtherVerdicts(t *testing.T) {
	api := &fakeRejecter{}
	if err := NewResolver(api).Resolve(t.Context(), []source.AlertRef{{ID: "iss-1"}},
		source.Verdict{Kind: "remediated"}); err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if len(api.rejected) != 0 {
		t.Error("Resolve() rejected issues for a non-ignore verdict")
	}
}

// One failing rejection does not stop the rest, and the failure surfaces.
func TestResolverJoinsErrors(t *testing.T) {
	api := &fakeRejecter{fail: map[string]error{"iss-1": errors.New("boom")}}
	err := NewResolver(api).Resolve(t.Context(),
		[]source.AlertRef{{ID: "iss-1"}, {ID: "iss-2"}},
		source.Verdict{Kind: source.VerdictIgnore})
	if err == nil {
		t.Fatal("Resolve() = nil, want the failure surfaced")
	}
	if len(api.rejected) != 1 || api.rejected[0].id != "iss-2" {
		t.Errorf("rejected = %+v, want iss-2 despite iss-1 failing", api.rejected)
	}
}
