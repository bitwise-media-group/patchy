// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package templates

// WizKV is one ordered key/value fact. Wiz hands resource tags over as
// unordered maps; the source sorts them into slices so the rendered markdown
// is stable and the goldens mean something.
type WizKV struct {
	Key   string
	Value string
}

// WizEntity describes the affected cloud resource as Wiz snapshotted it.
type WizEntity struct {
	// Name is the normalized provider identifier (the Finding's cloud
	// resource name).
	Name string
	// DisplayName is the resource's human-facing name.
	DisplayName string
	// Type is the provider's own type name.
	Type string
	// Platform is Wiz's platform value (GCP, AWS, Azure).
	Platform string
	// Subscription is the enclosing cloud account, with its display name.
	Subscription     string
	SubscriptionName string
	// Region the resource lives in.
	Region string
	// ConsoleURL links to the resource in its provider's console.
	ConsoleURL string
	// Tags are the provider tags/labels on the resource, sorted by key.
	Tags []WizKV
}

// WizIssue is the data the Wiz Issues description template renders — a
// flattened, presentation-ready view of the delivery. The source decides
// what is worth showing, the template decides how it looks.
type WizIssue struct {
	// ID is the Wiz issue id.
	ID string
	// ControlName and ControlID identify the security control that fired.
	ControlName string
	ControlID   string
	// Severity is Wiz's own rating, lowercased for display.
	Severity string
	// Status is the issue's lifecycle status.
	Status string
	// Description is the control's prose explanation.
	Description string
	// Recommendation is Wiz's remediation guidance, already markdown.
	Recommendation string
	// Entity is the affected resource.
	Entity WizEntity
	// Projects names the Wiz projects the entity belongs to.
	Projects string
	// CreatedAt is when Wiz opened the issue.
	CreatedAt string
	// URL links to the issue in the Wiz console.
	URL string
}

// WizThreat is the data the Wiz Defend description template renders.
type WizThreat struct {
	// ID is the Wiz threat id.
	ID string
	// Name is the threat's title.
	Name string
	// RuleName and RuleID identify the detection rule.
	RuleName string
	RuleID   string
	// Severity is Wiz's own rating, lowercased for display.
	Severity string
	// Status is the threat's lifecycle status.
	Status string
	// Description explains what was detected.
	Description string
	// Entity is the affected resource this Finding was scoped to.
	Entity WizEntity
	// Actors are the identities involved, rendered "name (type)".
	Actors []string
	// MitreTactics and MitreTechniques map the detection onto ATT&CK.
	MitreTactics    []string
	MitreTechniques []string
	// Detections counts the individual detections rolled into the threat.
	Detections int
	// CloudAccounts are the accounts involved.
	CloudAccounts []string
	// CreatedAt is when the threat was raised.
	CreatedAt string
	// URL links to the threat in the Wiz console.
	URL string
}

// RenderWizIssueDescription renders a Wiz issue as the Finding's markdown
// description — the body a human reads on the tracking issue and the handoff
// an agent investigates.
func RenderWizIssueDescription(i WizIssue) (string, error) {
	return render("finding_wiz_issue.md.tmpl", i)
}

// RenderWizThreatDescription renders a Wiz Defend threat as the Finding's
// markdown description.
func RenderWizThreatDescription(t WizThreat) (string, error) {
	return render("finding_wiz_threat.md.tmpl", t)
}
