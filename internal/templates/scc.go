// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package templates

// SCCKV is one ordered key/value fact. Security Command Center hands several
// of its richest fields over as unordered maps; the source sorts them into
// slices so the rendered markdown is stable and the goldens mean something.
type SCCKV struct {
	Key   string
	Value string
}

// SCCCompliance is one standard a finding breaches.
type SCCCompliance struct {
	Standard string
	Version  string
	IDs      []string
}

// SCCResource describes the affected cloud resource.
type SCCResource struct {
	Name        string
	DisplayName string
	Type        string
	Project     string
	Location    string
	Service     string
}

// SCCFinding is the data the Security Command Center description template
// renders. It is a flattened, presentation-ready view of the notification —
// the source decides what is worth showing, the template decides how it looks.
type SCCFinding struct {
	// Name is the finding's full SCC resource name.
	Name string
	// Category is the detector's raw finding type (PUBLIC_BUCKET_ACL).
	Category string
	// Class is the finding class (VULNERABILITY, MISCONFIGURATION, ...).
	Class string
	// Severity is SCC's own rating, for display alongside patchy's.
	Severity string
	// Description is the detector's prose explanation.
	Description string
	// NextSteps is the detector's remediation guidance, already markdown.
	NextSteps string
	// Resource is the affected cloud resource.
	Resource SCCResource
	// CVE and CVSS are set when the finding is a vulnerability.
	CVE  string
	CVSS string
	// MitreTactic and MitreTechniques are the ATT&CK mapping, when present.
	MitreTactic     string
	MitreTechniques []string
	// Compliances are the standards breached.
	Compliances []SCCCompliance
	// Properties are the detector's own facts, sorted by key.
	Properties []SCCKV
	// Marks are the operator's security marks, sorted by key. They are worth
	// showing: they are how a human annotates a resource, and one of them may
	// be the very label that failed to resolve a repository.
	Marks []SCCKV
	// DetectedAt is the event time.
	DetectedAt string
	// ExternalURI links to the finding in the Cloud Console.
	ExternalURI string
}

// RenderSCCDescription renders a Security Command Center notification as the
// Finding's markdown description. That description is the finding body a human
// reads on the tracking issue and the handoff file an agent investigates, so
// it carries the detector's own reasoning rather than a patchy summary of it.
func RenderSCCDescription(f SCCFinding) (string, error) {
	return render("finding_gcp_scc.md.tmpl", f)
}
