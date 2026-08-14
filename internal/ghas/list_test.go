// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghas

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// fakeEnumerator yields a fixed (repo, alert) sequence.
type fakeEnumerator struct {
	hits []struct {
		repo  ghclient.Repo
		alert *ghclient.Alert
	}
	complete bool
	err      error
	repos    []string // last filter received
}

func (f *fakeEnumerator) Enumerate(
	_ context.Context, repos []string, yield func(ghclient.Repo, *ghclient.Alert) bool,
) (bool, error) {
	f.repos = repos
	if f.err != nil {
		return false, f.err
	}
	for _, h := range f.hits {
		if !yield(h.repo, h.alert) {
			return false, nil
		}
	}
	return f.complete, nil
}

func enumeratorOf(repos ...ghclient.Repo) *fakeEnumerator {
	e := &fakeEnumerator{complete: true}
	for _, r := range repos {
		e.hits = append(e.hits, struct {
			repo  ghclient.Repo
			alert *ghclient.Alert
		}{r, testAlert()})
	}
	return e
}

// The backfill mapping is the webhook mapping: FindingFromAlert feeds both,
// so a listed alert must come out identical to a delivered one.
func TestListMapsLikeFindings(t *testing.T) {
	repo := ghclient.Repo{Owner: "acme", Name: "shop"}
	h := NewLister(enumeratorOf(repo))

	var got []source.Finding
	complete, err := h.List(context.Background(), nil, func(f source.Finding) bool {
		got = append(got, f)
		return true
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !complete {
		t.Error("List() complete = false, want true")
	}
	want := []source.Finding{FindingFromAlert(repo, testAlert())}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %+v\nwant %+v", got, want)
	}
	if want[0].Source != ID || want[0].AlertNumber != 7 {
		t.Errorf("FindingFromAlert() mapped source %q number %d, want %q 7", want[0].Source, want[0].AlertNumber, ID)
	}
}

func TestListFilters(t *testing.T) {
	repos := []ghclient.Repo{
		{Owner: "acme", Name: "shop"},
		{Owner: "acme", Name: "site"},
		{Owner: "acmex", Name: "shop"},
		{Owner: "beta", Name: "app"},
	}
	tests := []struct {
		name   string
		filter []string
		want   []string
	}{
		{"empty filter keeps everything", nil,
			[]string{"acme/shop", "acme/site", "acmex/shop", "beta/app"}},
		{"owner prefix", []string{"acme/"}, []string{"acme/shop", "acme/site"}},
		{"slash-less entry is an owner, not a substring", []string{"acme"},
			[]string{"acme/shop", "acme/site"}},
		{"exact repo", []string{"acme/shop"}, []string{"acme/shop"}},
		{"case-insensitive", []string{"ACME/Shop"}, []string{"acme/shop"}},
		{"several entries", []string{"beta/", "acme/site"}, []string{"acme/site", "beta/app"}},
		{"no match", []string{"gamma/"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewLister(enumeratorOf(repos...))
			var got []string
			if _, err := h.List(context.Background(), tt.filter, func(f source.Finding) bool {
				got = append(got, f.Repo.String())
				return true
			}); err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("List(%v) kept %v, want %v", tt.filter, got, tt.want)
			}
		})
	}
}

// The filter reaches the enumerator too — it prunes whole installations
// there — and a caller stop or enumerator truncation surfaces as
// complete=false.
func TestListPropagation(t *testing.T) {
	e := enumeratorOf(ghclient.Repo{Owner: "acme", Name: "shop"}, ghclient.Repo{Owner: "acme", Name: "site"})
	h := NewLister(e)
	filter := []string{"acme/"}

	complete, err := h.List(context.Background(), filter, func(source.Finding) bool { return false })
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if complete {
		t.Error("List() complete = true after caller stop, want false")
	}
	if !reflect.DeepEqual(e.repos, filter) {
		t.Errorf("enumerator received filter %v, want %v", e.repos, filter)
	}

	e.complete = false
	if complete, err = h.List(context.Background(), nil,
		func(source.Finding) bool { return true }); err != nil || complete {
		t.Errorf("List() over truncated enumerator = (%v, %v), want (false, nil)", complete, err)
	}

	e.err = errors.New("boom")
	if _, err = h.List(context.Background(), nil,
		func(source.Finding) bool { return true }); err == nil {
		t.Error("List() error = nil, want the enumerator's")
	}
}
