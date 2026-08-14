// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// ID is the source identifier stamped into the security-source label.
const ID = "ghas"

// EventType is the webhook event this source consumes.
const EventType = "code_scanning_alert"

// actionable are the alert actions that produce a finding. Everything else
// (fixed, closed_by_user, appeared_in_branch, ...) is state GitHub manages
// or noise this pipeline does not act on in v1.
var actionable = map[string]bool{
	"created":  true,
	"reopened": true,
}

// AlertGetter fetches the full code-scanning alert; the webhook payload
// carries a summary, but the rule help markdown only comes from the API.
type AlertGetter interface {
	GetAlert(ctx context.Context, repo ghclient.Repo, number int) (*ghclient.Alert, error)
}

// AlertDismisser closes a code-scanning alert on the forge — the write-back
// half of the source, exercised when the investigation returns "ignore".
type AlertDismisser interface {
	DismissAlert(ctx context.Context, repo ghclient.Repo, number int, reason, comment string) error
}

// Handler is the GHAS source plugin. It serves three directions: ingest,
// built with New; the verdict write-back, built with NewResolver; and the
// backfill listing, built with NewLister. They are separate constructors
// because the repository is per-delivery on the way in (it comes from the
// payload), per-finding on the way out, and the credential's whole scope
// for a backfill.
type Handler struct {
	alerts  AlertGetter
	dismiss AlertDismisser
	repo    ghclient.Repo
	enum    AlertEnumerator
}

// The handler serves both halves of the seam.
var (
	_ source.Handler  = (*Handler)(nil)
	_ source.Resolver = (*Handler)(nil)
)

// New builds the ingest handler around an alert fetcher (typically an adapter
// that resolves the right installation client per repository).
func New(alerts AlertGetter) *Handler { return &Handler{alerts: alerts} }

// NewResolver builds a handler for the write-back path, dismissing alerts on
// repo. A zero repo means the finding has no repository, so there is nothing
// on GitHub to dismiss and Resolve does nothing.
func NewResolver(d AlertDismisser, repo ghclient.Repo) *Handler {
	return &Handler{dismiss: d, repo: repo}
}

// ID implements source.Handler.
func (h *Handler) ID() string { return ID }

// Resolve implements source.Resolver: it dismisses every code-scanning alert
// it is handed. The caller groups alerts by source and passes only this
// handler's own, so a GHAS alert id is always a decimal alert number — one
// that is not is corrupt state worth surfacing, not another tool's identifier
// to step over.
func (h *Handler) Resolve(ctx context.Context, alerts []source.AlertRef, v source.Verdict) error {
	if v.Kind != source.VerdictIgnore || h.dismiss == nil {
		return nil
	}
	if h.repo.Owner == "" || h.repo.Name == "" {
		return nil // repo-less finding: nothing on GitHub to dismiss
	}
	reason, comment := v.Reason, v.Comment
	if reason == "" {
		reason = "false positive"
	}
	var errs []error
	for _, a := range alerts {
		num, err := strconv.Atoi(a.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("ghas: alert id %q is not a code-scanning alert number", a.ID))
			continue
		}
		if err := h.dismiss.DismissAlert(ctx, h.repo, num, reason, comment); err != nil {
			errs = append(errs, fmt.Errorf("ghas: dismiss alert %s#%d: %w", h.repo, num, err))
		}
	}
	return errors.Join(errs...)
}

// delivery is the slice of the code_scanning_alert payload we consume.
type delivery struct {
	Action string `json:"action"`
	Alert  struct {
		Number int `json:"number"`
	} `json:"alert"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

// Findings implements source.Handler: it normalizes one delivery, fetching
// the full alert from the API.
func (h *Handler) Findings(ctx context.Context, event string, payload []byte) ([]source.Finding, error) {
	if event != EventType {
		return nil, nil
	}
	var d delivery
	if err := json.Unmarshal(payload, &d); err != nil {
		return nil, fmt.Errorf("ghas: decode %s payload: %w", EventType, err)
	}
	if !actionable[d.Action] {
		return nil, nil
	}
	repo := ghclient.Repo{Owner: d.Repository.Owner.Login, Name: d.Repository.Name}
	if repo.Owner == "" || repo.Name == "" || d.Alert.Number == 0 {
		return nil, fmt.Errorf("ghas: %s payload missing repository or alert number", EventType)
	}

	alert, err := h.alerts.GetAlert(ctx, repo, d.Alert.Number)
	if err != nil {
		return nil, fmt.Errorf("ghas: fetch alert %s#%d: %w", repo, d.Alert.Number, err)
	}

	return []source.Finding{FindingFromAlert(repo, alert)}, nil
}

// FindingFromAlert normalizes one code-scanning alert into the
// source-agnostic finding shape — the single mapping both the webhook path
// and the backfill lister go through.
func FindingFromAlert(repo ghclient.Repo, alert *ghclient.Alert) source.Finding {
	f := source.Finding{
		Source:      ID,
		Repo:        source.Repo{Owner: repo.Owner, Name: repo.Name},
		AlertNumber: alert.Number,
		Advisories:  advisories(alert),
		RuleID:      alert.RuleID,
		Title:       title(alert),
		Description: description(alert),
		Severity:    normalizeSeverity(alert.Severity),
		HTMLURL:     alert.HTMLURL,
	}
	if alert.Path != "" {
		f.Locations = []source.Location{{
			Path:      alert.Path,
			StartLine: alert.StartLine,
			EndLine:   alert.EndLine,
			Snippet:   alert.Snippet,
		}}
	}
	return f
}

// advisories extracts the categorization IDs, most authoritative first:
// GHSA, then CVE, then CWEs from CodeQL rule tags ("external/cwe/cwe-079" →
// "CWE-79"). A rule with no recognizable advisory falls back to
// "rule:<rule id>" so accumulation still has a stable key.
func advisories(a *ghclient.Alert) []string {
	var ghsas, cves, cwes []string
	for _, tag := range a.Tags {
		id := strings.ToUpper(tag[strings.LastIndex(tag, "/")+1:])
		switch {
		case strings.HasPrefix(id, "GHSA-"):
			ghsas = append(ghsas, id)
		case strings.HasPrefix(id, "CVE-"):
			cves = append(cves, id)
		case strings.HasPrefix(id, "CWE-"):
			cwes = append(cwes, normalizeCWE(id))
		}
	}
	out := make([]string, 0, len(ghsas)+len(cves)+len(cwes)+1)
	out = append(out, ghsas...)
	out = append(out, cves...)
	out = append(out, cwes...)
	if len(out) == 0 {
		out = append(out, "rule:"+a.RuleID)
	}
	return out
}

// normalizeCWE strips zero-padding: CWE-079 → CWE-79.
func normalizeCWE(id string) string {
	num := strings.TrimPrefix(id, "CWE-")
	if n, err := strconv.Atoi(num); err == nil {
		return "CWE-" + strconv.Itoa(n)
	}
	return id
}

// normalizeSeverity maps GHAS severities to the label scale. The alert
// carries either a security_severity_level (already low/medium/high/critical)
// or a raw rule severity (none/note/warning/error).
func normalizeSeverity(s string) string {
	switch strings.ToLower(s) {
	case "low", "medium", "high", "critical":
		return strings.ToLower(s)
	case "error":
		return "high"
	case "warning":
		return "medium"
	default:
		return "low"
	}
}

func title(a *ghclient.Alert) string {
	if a.RuleDescription != "" {
		return a.RuleDescription
	}
	return a.RuleID
}

func description(a *ghclient.Alert) string {
	if a.RuleHelp != "" {
		return a.RuleHelp
	}
	return a.RuleDescription
}
