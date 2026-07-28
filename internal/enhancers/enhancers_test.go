// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bitwise-media-group/patchy/pkg/enhance"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

const staticYAML = `
repos:
    acme/shop:
        owners: [octocat, hubot]
        attributes:
            system: storefront
            tier: "1"
        markdown: |
            Storefront is PCI-scoped.
    acme/api: {}
`

func writeStatic(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "context.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStaticFile(t *testing.T) {
	s, err := NewStaticFile(writeStatic(t, staticYAML))
	if err != nil {
		t.Fatalf("NewStaticFile() error = %v", err)
	}

	t.Run("known repo", func(t *testing.T) {
		enr, err := s.Enhance(context.Background(), enhance.Issue{
			Repo: source.Repo{Owner: "acme", Name: "shop"},
		})
		if err != nil {
			t.Fatalf("Enhance() error = %v", err)
		}
		if want := []string{"octocat", "hubot"}; !slices.Equal(enr.Owners, want) {
			t.Errorf("Owners = %v, want %v", enr.Owners, want)
		}
		// Markdown carries only the free-form entry content; owners and
		// attributes stay structured.
		if want := "Storefront is PCI-scoped."; enr.CommentMarkdown != want {
			t.Errorf("CommentMarkdown = %q, want %q", enr.CommentMarkdown, want)
		}
		if enr.Attributes["system"] != "storefront" || enr.Attributes["tier"] != "1" {
			t.Errorf("Attributes = %v", enr.Attributes)
		}
	})

	t.Run("unknown repo contributes nothing", func(t *testing.T) {
		enr, err := s.Enhance(context.Background(), enhance.Issue{
			Repo: source.Repo{Owner: "acme", Name: "other"},
		})
		if err != nil || enr != nil {
			t.Errorf("Enhance() = %v, %v; want nil, nil", enr, err)
		}
	})
}

func TestStaticFileErrors(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"missing file", filepath.Join(t.TempDir(), "absent.yaml")},
		{"bad yaml", writeStatic(t, "repos: [")},
		{"unknown keys", writeStatic(t, "repositories:\n    a/b: {}\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewStaticFile(tt.path); err == nil {
				t.Error("NewStaticFile() error = nil, want error")
			}
		})
	}
}
