// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/bitwise-media-group/patchy/pkg/enhance"
)

// StaticFile enhances issues from a YAML map of repositories to ownership,
// attributes, and optional free-form markdown — the stand-in for a CMDB:
//
//	repos:
//	    acme/shop:
//	        owners: [octocat, hubot]
//	        attributes:
//	            system: storefront
//	            tier: "1"
//	        markdown: |
//	            Storefront is PCI-scoped; page #payments-oncall first.
type StaticFile struct {
	repos map[string]staticEntry
}

type staticEntry struct {
	Owners     []string          `yaml:"owners"`
	Attributes map[string]string `yaml:"attributes"`
	Markdown   string            `yaml:"markdown"`
}

// NewStaticFile loads the map from path.
func NewStaticFile(path string) (*StaticFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("static context: %w", err)
	}
	var doc struct {
		Repos map[string]staticEntry `yaml:"repos"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("static context %s: %w", path, err)
	}
	return &StaticFile{repos: doc.Repos}, nil
}

// ID implements enhance.Enhancer.
func (*StaticFile) ID() string { return "static-context" }

// Enhance implements enhance.Enhancer; a repository absent from the map
// contributes nothing.
func (s *StaticFile) Enhance(_ context.Context, issue enhance.Issue) (*enhance.Enrichment, error) {
	entry, ok := s.repos[issue.Repo.String()]
	if !ok {
		return nil, nil
	}
	return &enhance.Enrichment{
		Owners:          entry.Owners,
		CommentMarkdown: strings.TrimRight(entry.Markdown, "\n"),
		Attributes:      entry.Attributes,
	}, nil
}
