// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"strings"

	"github.com/bitwise-media-group/patchy/pkg/source"
)

// repositoryFrom reads the ownership labels (or tags — the vocabulary is
// shared across clouds), or nil when the resource carries none. The URL form
// wins: it is the only one that can name a self-hosted forge, so a resource
// carrying it means it deliberately.
func repositoryFrom(labels map[string]string, keys LabelKeys, provider, host string) *source.RepositoryRef {
	if len(labels) == 0 {
		return nil
	}
	if p := labels[keys.Provider]; p != "" {
		provider = p
	}
	if u := labels[keys.URL]; u != "" {
		return &source.RepositoryRef{Provider: provider, URL: normalizeURL(u)}
	}
	org, name := labels[keys.Org], labels[keys.Name]
	if org == "" || name == "" {
		return nil
	}
	return &source.RepositoryRef{
		Provider: provider,
		Owner:    org,
		Name:     name,
		URL:      "https://" + host + "/" + org + "/" + name,
	}
}

// normalizeURL accepts a bare host/path as well as a full URL, because a
// Google Cloud label cannot hold "://" and an operator working around that
// will write the value without a scheme.
func normalizeURL(raw string) string {
	if strings.Contains(raw, "://") {
		return raw
	}
	return "https://" + strings.TrimPrefix(raw, "//")
}

// or returns v, or fallback when v is empty.
func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
