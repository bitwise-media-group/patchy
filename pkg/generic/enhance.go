// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package generic

import "github.com/bitwise-media-group/patchy/pkg/source"

// Issue is the view of a finding an external enhancer receives — the same
// projection in-tree enhancers see, JSON-tagged for the wire.
type Issue struct {
	// Repo is the repository the finding was raised against; absent for a
	// cloud finding whose repository is not yet resolved.
	Repo *Repo `json:"repo,omitempty"`
	// Number is the tracking issue number, when one exists.
	Number int `json:"number,omitempty"`
	// Title is the finding title.
	Title string `json:"title"`
	// Body is the finding description markdown.
	Body string `json:"body,omitempty"`
	// Labels are the tracking labels.
	Labels []string `json:"labels,omitempty"`
	// CloudResource is set when the finding is about a cloud resource
	// rather than repository code.
	CloudResource *source.CloudResource `json:"cloudResource,omitempty"`
}

// EnhanceRequest is what patchy POSTs to the integration's enhancer URL.
type EnhanceRequest struct {
	// Version is the contract version, always Version.
	Version string `json:"version"`
	// Integration is the requesting Integration's name, so one process can
	// serve several integrations from a single endpoint.
	Integration string `json:"integration"`
	// Issue is the finding under enhancement.
	Issue Issue `json:"issue"`
}

// EnhanceResponse is the enhancer's contribution. Answering 204, or 200 with
// an empty body, contributes nothing — the honest reply for a finding the
// process has no context on.
type EnhanceResponse struct {
	// Owners are logins responsible for the finding, in preference order;
	// the pipeline uses them for issue assignment.
	Owners []string `json:"owners,omitempty"`
	// CommentMarkdown is arbitrary content, kept as one sticky comment on
	// the tracking issue, attributed to this integration. Semi-structured
	// facts belong in Attributes.
	CommentMarkdown string `json:"commentMarkdown,omitempty"`
	// Attributes are semi-structured facts (system name, environment,
	// tier), projected as tracking labels.
	Attributes map[string]string `json:"attributes,omitempty"`
	// Repository resolves the repository of a finding that arrived without
	// one. Ignored when the finding already has a repository; only
	// provider "github" is honoured today.
	Repository *source.RepositoryRef `json:"repository,omitempty"`
}
