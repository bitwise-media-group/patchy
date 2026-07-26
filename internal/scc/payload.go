// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scc

// The decoded slices of the Pub/Sub push envelope and the SCC
// NotificationMessage inside it. Only the fields patchy consumes are declared;
// a real notification carries dozens more, and the finding's markdown
// rendering reads them from the generic map rather than growing this file.

// pushEnvelope is the body Pub/Sub POSTs to a push endpoint.
type pushEnvelope struct {
	Message struct {
		// Data is the base64-encoded NotificationMessage.
		Data string `json:"data"`
		// MessageID is the delivery's unique id, used for dedup. Pub/Sub
		// sends both spellings; messageId is the documented one.
		MessageID string `json:"messageId"`
		SnakeID   string `json:"message_id"`
		PublishAt string `json:"publishTime"`
		// DeliveryAttempt counts redeliveries; present only when the
		// subscription has a dead-letter policy.
		DeliveryAttempt int `json:"deliveryAttempt"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

// id returns the delivery id, tolerating either spelling.
func (e *pushEnvelope) id() string {
	if e.Message.MessageID != "" {
		return e.Message.MessageID
	}
	return e.Message.SnakeID
}

// notification is the SCC NotificationMessage.
type notification struct {
	NotificationConfigName string   `json:"notificationConfigName"`
	Finding                finding  `json:"finding"`
	Resource               resource `json:"resource"`
}

// finding is the slice of the SCC Finding we consume.
type finding struct {
	// Name is the full resource name of the finding itself:
	// organizations/<org>/sources/<src>/findings/<id>. It is the alert id.
	Name string `json:"name"`
	// CanonicalName is the same finding named from its own project/folder.
	CanonicalName string `json:"canonicalName"`
	// Parent is the source that produced the finding.
	Parent string `json:"parent"`
	// ResourceName is the full name of the affected cloud resource.
	ResourceName string `json:"resourceName"`
	// State is ACTIVE or INACTIVE.
	State string `json:"state"`
	// Category is the detector's finding type, e.g. PUBLIC_BUCKET_ACL.
	Category string `json:"category"`
	// ExternalURI links to the finding in the provider's console.
	ExternalURI string `json:"externalUri"`
	// Severity is CRITICAL, HIGH, MEDIUM, LOW, or SEVERITY_UNSPECIFIED.
	Severity string `json:"severity"`
	// Mute is MUTED, UNMUTED, or UNDEFINED.
	Mute string `json:"mute"`
	// FindingClass is VULNERABILITY, THREAT, MISCONFIGURATION, OBSERVATION,
	// SCC_ERROR, POSTURE_VIOLATION, or TOXIC_COMBINATION.
	FindingClass string `json:"findingClass"`
	// Description explains the issue.
	Description string `json:"description"`
	// NextSteps is the detector's remediation guidance, in markdown.
	NextSteps string `json:"nextSteps"`
	// ModuleName is the detector module that fired.
	ModuleName string `json:"moduleName"`
	// EventTime is when the event was detected.
	EventTime string `json:"eventTime"`
	// CreateTime is when SCC first recorded the finding.
	CreateTime string `json:"createTime"`
	// Vulnerability carries CVE data when the finding is one.
	Vulnerability *vulnerability `json:"vulnerability"`
	// MitreAttack maps the finding onto the ATT&CK framework.
	MitreAttack *mitreAttack `json:"mitreAttack"`
	// Compliances are the standards the finding breaches.
	Compliances []compliance `json:"compliances"`
	// SourceProperties are detector-specific facts, shape varying by detector.
	SourceProperties map[string]any `json:"sourceProperties"`
	// SecurityMarks are the operator's own annotations on the finding.
	SecurityMarks *securityMarks `json:"securityMarks"`
}

// vulnerability carries the CVE identification.
type vulnerability struct {
	CVE *struct {
		ID              string `json:"id"`
		UpstreamFixTime string `json:"upstreamFixAvailable"`
		Cvssv3          *struct {
			BaseScore float64 `json:"baseScore"`
		} `json:"cvssv3"`
	} `json:"cve"`
}

// mitreAttack is the ATT&CK mapping.
type mitreAttack struct {
	PrimaryTactic     string   `json:"primaryTactic"`
	PrimaryTechniques []string `json:"primaryTechniques"`
	Version           string   `json:"version"`
}

// compliance is one standard the finding breaches.
type compliance struct {
	Standard string   `json:"standard"`
	Version  string   `json:"version"`
	IDs      []string `json:"ids"`
}

// securityMarks are the operator's key/value annotations.
type securityMarks struct {
	Name  string            `json:"name"`
	Marks map[string]string `json:"marks"`
}

// resource is the affected cloud resource, as SCC describes it. Note it
// carries no arbitrary GCP resource labels — those live on the resource
// itself and reach patchy only through a Cloud Asset Inventory lookup.
type resource struct {
	Name               string   `json:"name"`
	DisplayName        string   `json:"displayName"`
	Type               string   `json:"type"`
	Project            string   `json:"project"`
	ProjectDisplayName string   `json:"projectDisplayName"`
	Parent             string   `json:"parent"`
	ParentDisplayName  string   `json:"parentDisplayName"`
	Location           string   `json:"location"`
	Service            string   `json:"service"`
	CloudProvider      string   `json:"cloudProvider"`
	Folders            []folder `json:"folders"`
}

// folder is one ancestor folder of the resource.
type folder struct {
	ResourceFolder            string `json:"resourceFolder"`
	ResourceFolderDisplayName string `json:"resourceFolderDisplayName"`
}
