// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package templates

// GitHubSecretLocation is one place a leaked secret was found. Path is set
// for a location in a repository file; the other kinds (an issue body, a
// pull request comment) carry only a link.
type GitHubSecretLocation struct {
	// Kind is GitHub's location type ("commit", "issue_body", ...).
	Kind      string
	Path      string
	StartLine int
	CommitSHA string
	URL       string
}

// GitHubSecret is the data the secret-scanning description template
// renders. It has no field for the secret value, by design: the description
// is the tracking-issue body and the agent's handoff file.
type GitHubSecret struct {
	// DisplayName is GitHub's human name for the secret type.
	DisplayName string
	// SecretType is the machine id, e.g. github_personal_access_token.
	SecretType string
	// Provider is the issuing service, when GitHub names one.
	Provider string
	// Validity is GitHub's live check: active, inactive or unknown.
	Validity string
	// PubliclyLeaked, MultiRepo and PushProtectionBypassed are GitHub's
	// flags on the alert.
	PubliclyLeaked         bool
	MultiRepo              bool
	PushProtectionBypassed bool
	// Locations are where the secret was found.
	Locations []GitHubSecretLocation
	// MoreLocations counts locations left out of Locations.
	MoreLocations int
	// URL links to the alert on GitHub.
	URL string
}

// RenderGitHubSecretDescription renders a secret-scanning alert as the
// Finding's markdown description, leading with the rotation-first guidance
// that sets it apart from a code flaw.
func RenderGitHubSecretDescription(s GitHubSecret) (string, error) {
	return render("finding_github_secret.md.tmpl", s)
}
