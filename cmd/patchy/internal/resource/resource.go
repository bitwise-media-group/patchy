// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package resource

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// Kind describes one noun the CLI understands.
type Kind struct {
	// Singular is the canonical spelling, used in messages ("finding fnd-1").
	Singular string
	// Plural is the API resource name, and what the URL path uses.
	Plural string
	// Aliases are the other accepted spellings: the plural, plus the CRD's
	// declared shortName.
	Aliases []string
	// New returns an empty object of the kind.
	New func() client.Object
	// NewList returns an empty list of the kind.
	NewList func() client.ObjectList
}

// Kinds are the nouns, in the order `patchy api-resources`-style help lists
// them: the pipeline state machine first, then its configuration.
var Kinds = []Kind{
	{
		Singular: "finding",
		Plural:   "findings",
		Aliases:  []string{"fnd"},
		New:      func() client.Object { return &v1alpha1.Finding{} },
		NewList:  func() client.ObjectList { return &v1alpha1.FindingList{} },
	},
	{
		Singular: "investigation",
		Plural:   "investigations",
		Aliases:  []string{"inv"},
		New:      func() client.Object { return &v1alpha1.Investigation{} },
		NewList:  func() client.ObjectList { return &v1alpha1.InvestigationList{} },
	},
	{
		Singular: "remediation",
		Plural:   "remediations",
		Aliases:  []string{"rem"},
		New:      func() client.Object { return &v1alpha1.Remediation{} },
		NewList:  func() client.ObjectList { return &v1alpha1.RemediationList{} },
	},
	{
		Singular: "findingrollup",
		Plural:   "findingrollups",
		// "rollup" is a CLI-only convenience; "fr" is the CRD's shortName.
		Aliases: []string{"fr", "rollup", "rollups"},
		New:     func() client.Object { return &v1alpha1.FindingRollup{} },
		NewList: func() client.ObjectList { return &v1alpha1.FindingRollupList{} },
	},
	{
		Singular: "repository",
		Plural:   "repositories",
		Aliases:  []string{"repo", "repos"},
		New:      func() client.Object { return &v1alpha1.Repository{} },
		NewList:  func() client.ObjectList { return &v1alpha1.RepositoryList{} },
	},
	{
		Singular: "integration",
		Plural:   "integrations",
		New:      func() client.Object { return &v1alpha1.Integration{} },
		NewList:  func() client.ObjectList { return &v1alpha1.IntegrationList{} },
	},
	{
		Singular: "forge",
		Plural:   "forges",
		New:      func() client.Object { return &v1alpha1.Forge{} },
		NewList:  func() client.ObjectList { return &v1alpha1.ForgeList{} },
	},
}

// Lookup resolves a user-typed noun. Matching is case-insensitive and accepts
// the singular, the plural, or any declared alias.
func Lookup(noun string) (Kind, error) {
	want := strings.ToLower(strings.TrimSpace(noun))
	for _, k := range Kinds {
		if want == k.Singular || want == k.Plural {
			return k, nil
		}
		if slices.Contains(k.Aliases, want) {
			return k, nil
		}
	}
	return Kind{}, fmt.Errorf("unknown resource %q; try one of: %s", noun, strings.Join(Names(), ", "))
}

// Names lists the canonical singular of every kind, for help and error text.
func Names() []string {
	out := make([]string, 0, len(Kinds))
	for _, k := range Kinds {
		out = append(out, k.Singular)
	}
	return out
}

// Spellings lists every accepted spelling, sorted — the completion source.
func Spellings() []string {
	var out []string
	for _, k := range Kinds {
		out = append(out, k.Singular, k.Plural)
		out = append(out, k.Aliases...)
	}
	sort.Strings(out)
	return out
}

// Run reports whether the kind is an agent run (an Investigation or
// Remediation): the two kinds that own a report, an attempt ordinal and a
// deterministic child name, which is what `review` needs to know.
func (k Kind) Run() bool {
	return k.Singular == "investigation" || k.Singular == "remediation"
}
