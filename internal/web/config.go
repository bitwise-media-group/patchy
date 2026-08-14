// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/integrationcap"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

// ConfigDataset is the payload behind GET /api/config: the configured
// Forges, Integrations and enhancers the configuration view renders. It
// mirrors ui/src/types.ts. Never public — the route requires a signed-in
// identity holding native get on integrations.
type ConfigDataset struct {
	GeneratedAt  string              `json:"generatedAt"`
	Namespace    string              `json:"namespace,omitempty"`
	Forges       []ForgeConfig       `json:"forges"`
	Integrations []IntegrationConfig `json:"integrations"`
	Enhancers    []EnhancerConfig    `json:"enhancers"`
}

// ForgeConfig is one Forge's configuration surface.
type ForgeConfig struct {
	Name         string   `json:"name"`
	Provider     string   `json:"provider"`
	BaseURL      string   `json:"baseURL,omitempty"`
	Orgs         []string `json:"orgs,omitempty"`
	Repositories []string `json:"repositories,omitempty"`
	SecretRef    string   `json:"secretRef,omitempty"`
	Interval     string   `json:"interval,omitempty"`
	Suspend      bool     `json:"suspend,omitempty"`
	// Ready is the Ready condition's status (True/False/Unknown); empty
	// when the controller has not reported yet.
	Ready string `json:"ready,omitempty"`
	// ReadyMessage carries the condition message when not ready.
	ReadyMessage string `json:"readyMessage,omitempty"`
}

// IntegrationConfig is one Integration's configuration surface, including
// the backfill trigger's state.
type IntegrationConfig struct {
	Name         string   `json:"name"`
	Provider     string   `json:"provider"`
	WebhookPath  string   `json:"webhookPath,omitempty"`
	SecretRef    string   `json:"secretRef,omitempty"`
	Interval     string   `json:"interval,omitempty"`
	Suspend      bool     `json:"suspend,omitempty"`
	Ready        string   `json:"ready,omitempty"`
	ReadyMessage string   `json:"readyMessage,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	// Redelivery reports the last failed-delivery sweep.
	Redelivery *RedeliveryStatus `json:"redelivery,omitempty"`
	// Backfill reports the last manual backfill and any pending request.
	Backfill *BackfillStatus `json:"backfill,omitempty"`
	// BackfillSupported reports whether the backfill trigger can do
	// anything here (github provider with code scanning enabled).
	BackfillSupported bool `json:"backfillSupported,omitempty"`
}

// RedeliveryStatus mirrors status.redelivery.
type RedeliveryStatus struct {
	LastSweepAt string `json:"lastSweepAt,omitempty"`
	Scanned     int32  `json:"scanned,omitempty"`
	Redelivered int32  `json:"redelivered,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Error       string `json:"error,omitempty"`
}

// BackfillStatus mirrors status.backfill plus the pending-request echo the
// trigger button renders.
type BackfillStatus struct {
	LastRunAt string `json:"lastRunAt,omitempty"`
	Listed    int32  `json:"listed,omitempty"`
	Ingested  int32  `json:"ingested,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
	// RequestedBy/RequestedAt echo spec.backfill; Pending marks a request
	// the controller has not attempted yet — a failed attempt clears it so
	// the trigger re-enables for a corrected request.
	RequestedBy string `json:"requestedBy,omitempty"`
	RequestedAt string `json:"requestedAt,omitempty"`
	Pending     bool   `json:"pending,omitempty"`
}

// EnhancerConfig is one context-enhancer instance derived from the
// Integrations. The static-context enhancer is deliberately absent: it is
// a context-controller command-line flag, invisible from the cluster
// configuration this view projects.
type EnhancerConfig struct {
	// ID is the enhancer's attribution id, as it appears on enrichments.
	ID string `json:"id"`
	// Integration names the Integration carrying the enhancer's config.
	Integration string `json:"integration,omitempty"`
	Enabled     bool   `json:"enabled"`
	// Ambiguous marks a singleton capability enabled by more than one
	// Integration — a configuration error the enhancer chain refuses, so
	// the view surfaces it rather than picking one.
	Ambiguous bool `json:"ambiguous,omitempty"`
}

// handleConfig serves the configuration dataset to an identity holding
// native get on integrations. Never public: even the shape of the estate's
// configuration is operator information.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	id, err := s.auth.Identify(w, r)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "identify failed", slog.Any("error", err))
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}
	if id == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	grants, err := s.granter.Grants(r.Context(), *id)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "grants failed", slog.Any("error", err))
		http.Error(w, "authorization failed", http.StatusInternalServerError)
		return
	}
	if !grants.Config {
		http.Error(w, fmt.Sprintf("Permission denied. User %q may not view the configuration of namespace %q.",
			id.Display(), s.namespace), http.StatusForbidden)
		return
	}

	ds, err := s.buildConfigDataset(r.Context())
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "build config dataset", slog.Any("error", err))
		http.Error(w, "failed to load configuration", http.StatusInternalServerError)
		return
	}
	writeJSONGzip(w, r, ds)
}

// buildConfigDataset assembles the configuration projection from the
// cached client.
func (s *Server) buildConfigDataset(ctx context.Context) (*ConfigDataset, error) {
	ds := &ConfigDataset{
		GeneratedAt:  s.now().UTC().Format(time.RFC3339),
		Namespace:    s.namespace,
		Forges:       []ForgeConfig{},
		Integrations: []IntegrationConfig{},
		Enhancers:    []EnhancerConfig{},
	}

	var forges v1alpha1.ForgeList
	if err := s.client.List(ctx, &forges, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("list forges: %w", err)
	}
	sort.Slice(forges.Items, func(a, b int) bool { return forges.Items[a].Name < forges.Items[b].Name })
	for i := range forges.Items {
		ds.Forges = append(ds.Forges, projectForgeConfig(&forges.Items[i]))
	}

	var integs v1alpha1.IntegrationList
	if err := s.client.List(ctx, &integs, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("list integrations: %w", err)
	}
	sort.Slice(integs.Items, func(a, b int) bool { return integs.Items[a].Name < integs.Items[b].Name })
	for i := range integs.Items {
		ds.Integrations = append(ds.Integrations, projectIntegrationConfig(&integs.Items[i]))
	}
	ds.Enhancers = deriveEnhancers(integs.Items)
	return ds, nil
}

// projectForgeConfig flattens one Forge CR onto the wire type.
func projectForgeConfig(f *v1alpha1.Forge) ForgeConfig {
	out := ForgeConfig{
		Name:         f.Name,
		Provider:     string(f.Spec.Provider),
		BaseURL:      f.Spec.BaseURL,
		Orgs:         f.Spec.Orgs,
		Repositories: f.Spec.Repositories,
		SecretRef:    f.Spec.SecretRef.Name,
		Interval:     interval(f.Spec.Interval),
		Suspend:      f.Spec.Suspend,
	}
	out.Ready, out.ReadyMessage = readyCondition(f.Status.Conditions)
	return out
}

// projectIntegrationConfig flattens one Integration CR onto the wire type.
// Only the operator-facing configuration surface is projected — never the
// credential Secret's contents, and none of the never-written status
// fields (installations, rateLimit, lastEventAt).
func projectIntegrationConfig(integ *v1alpha1.Integration) IntegrationConfig {
	out := IntegrationConfig{
		Name:              integ.Name,
		Provider:          string(integ.Spec.Provider),
		WebhookPath:       integ.Status.WebhookPath,
		Interval:          interval(integ.Spec.Interval),
		Suspend:           integ.Spec.Suspend,
		Capabilities:      capabilities(integ),
		BackfillSupported: backfillSupported(integ),
	}
	if ref := integ.Spec.SecretRef; ref != nil {
		out.SecretRef = ref.Name
	}
	out.Ready, out.ReadyMessage = readyCondition(integ.Status.Conditions)
	if red := integ.Status.Redelivery; red != nil {
		out.Redelivery = &RedeliveryStatus{
			LastSweepAt: stampPtr(red.LastSweepAt),
			Scanned:     red.Scanned,
			Redelivered: red.Redelivered,
			Truncated:   red.Truncated,
			Error:       red.Error,
		}
	}
	out.Backfill = wireBackfill(integ)
	return out
}

// wireBackfill folds status.backfill and the spec.backfill echo into one
// wire block; nil when neither exists.
func wireBackfill(integ *v1alpha1.Integration) *BackfillStatus {
	st, req := integ.Status.Backfill, integ.Spec.Backfill
	if st == nil && req == nil {
		return nil
	}
	out := &BackfillStatus{}
	var echoed, attempted *metav1.Time
	if st != nil {
		out.LastRunAt = stampPtr(st.LastRunAt)
		out.Listed = st.Listed
		out.Ingested = st.Ingested
		out.Truncated = st.Truncated
		out.Error = st.Error
		echoed = st.BackfilledAt
		attempted = st.AttemptedAt
	}
	if req != nil {
		out.RequestedBy = req.By
		out.RequestedAt = stamp(req.At)
		// Pending disables the trigger, so it must clear once the controller
		// has attempted the request even when the walk failed (the controller
		// keeps retrying, but a failure like a bad repository filter needs a
		// corrected request, not a locked button). The backfilledAt echo alone
		// would pin a failed request pending forever.
		out.Pending = (echoed == nil || echoed.Time.Before(req.At.Time)) &&
			(attempted == nil || attempted.Time.Before(req.At.Time))
	}
	return out
}

// backfillSupported reports whether a backfill request could do anything on
// this Integration: the github provider with code-scanning ingestion
// enabled — the same gate the integration-controller's lister applies.
func backfillSupported(integ *v1alpha1.Integration) bool {
	return integ.Spec.Provider == v1alpha1.IntegrationProviderGitHub &&
		integ.Spec.GitHub != nil &&
		integ.Spec.GitHub.CodeScanningAlerts != nil &&
		integ.Spec.GitHub.CodeScanningAlerts.Enabled
}

// capabilities names the Integration's enabled capabilities for display.
func capabilities(integ *v1alpha1.Integration) []string {
	var out []string
	add := func(name string, enabled bool) {
		if enabled {
			out = append(out, name)
		}
	}
	spec := &integ.Spec
	if gh := spec.GitHub; gh != nil {
		add("issues", gh.Issues != nil && gh.Issues.Enabled)
		add("codeScanningAlerts", gh.CodeScanningAlerts != nil && gh.CodeScanningAlerts.Enabled)
		add("redelivery", gh.Redelivery != nil && gh.Redelivery.Enabled)
	}
	if gc := spec.GoogleCloud; gc != nil {
		add("securityCommandCenter", gc.SecurityCommandCenter != nil && gc.SecurityCommandCenter.Enabled)
		add("cloudAssetInventory", gc.CloudAssetInventory != nil && gc.CloudAssetInventory.Enabled)
	}
	if wz := spec.Wiz; wz != nil {
		add("issues", wz.Issues != nil && wz.Issues.Enabled)
		add("defend", wz.Defend != nil && wz.Defend.Enabled)
		add("api", wz.API != nil)
	}
	if aws := spec.AWS; aws != nil {
		add("resourceTags", aws.ResourceTags != nil && aws.ResourceTags.Enabled)
	}
	if az := spec.Azure; az != nil {
		add("resourceTags", az.ResourceTags != nil && az.ResourceTags.Enabled)
	}
	if gen := spec.Generic; gen != nil {
		add("source", gen.Source != nil && gen.Source.Enabled)
		add("enhance", gen.Enhance != nil && gen.Enhance.Enabled)
	}
	return out
}

// deriveEnhancers projects the enhancer instances the Integrations
// configure, via the same integrationcap predicates the context-controller
// selects by. The ids match the enrichment attribution ids
// (internal/enhancers). A singleton capability enabled by several
// Integrations is surfaced on every one of them as ambiguous rather than
// swallowed — the enhancer chain refuses that configuration too.
func deriveEnhancers(integs []v1alpha1.Integration) []EnhancerConfig {
	out := []EnhancerConfig{}
	singletons := []struct {
		id  string
		has integrationcap.Capability
	}{
		{"google-cloud-labels", integrationcap.CloudAssetInventoryEnabled},
		{"aws-resource-tags", integrationcap.AWSResourceTagsEnabled},
		{"azure-resource-tags", integrationcap.AzureResourceTagsEnabled},
	}
	for _, s := range singletons {
		var matches []string
		for i := range integs {
			if s.has(&integs[i]) {
				matches = append(matches, integs[i].Name)
			}
		}
		for _, name := range matches {
			out = append(out, EnhancerConfig{
				ID:          s.id,
				Integration: name,
				Enabled:     true,
				Ambiguous:   len(matches) > 1,
			})
		}
	}
	// The generic enhancer is one instance per Integration, attributed by
	// the Integration's own name — never ambiguous.
	for i := range integs {
		if integrationcap.GenericEnhanceEnabled(&integs[i]) {
			out = append(out, EnhancerConfig{
				ID:          "generic",
				Integration: integs[i].Name,
				Enabled:     true,
			})
		}
	}
	return out
}

// readyCondition extracts the Ready condition's status and (when not
// ready) message.
func readyCondition(conds []metav1.Condition) (status, message string) {
	c := meta.FindStatusCondition(conds, v1alpha1.ConditionReady)
	if c == nil {
		return "", ""
	}
	if c.Status != metav1.ConditionTrue {
		message = c.Message
	}
	return string(c.Status), message
}

// interval renders a revalidation interval; zero stays empty.
func interval(d metav1.Duration) string {
	if d.Duration <= 0 {
		return ""
	}
	return d.Duration.String()
}

// backfillBody is the POST /api/integrations/{name}/actions/backfill
// payload; the repository filter is optional.
type backfillBody struct {
	Repositories []string `json:"repositories"`
}

// Mirror the CRD's validation so a bad filter fails here with a message
// rather than as an opaque admission error.
const (
	maxBackfillRepos       = 50
	maxBackfillEntryLength = 200
)

// handleBackfill stamps spec.backfill on one Integration for an identity
// granted the backfill verb; the integration-controller performs the walk
// on its next reconcile (the status server holds no forge credential).
func (s *Server) handleBackfill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id, err := s.auth.Identify(w, r)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "identify failed", slog.Any("error", err))
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}
	if id == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	grants, err := s.granter.Grants(r.Context(), *id)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "grants failed", slog.Any("error", err))
		http.Error(w, "authorization failed", http.StatusInternalServerError)
		return
	}
	if !grants.AllowsIntegration(authz.VerbBackfill) {
		http.Error(w, fmt.Sprintf("Permission denied. User %q may not backfill integrations in namespace %q.",
			id.Display(), s.namespace), http.StatusForbidden)
		return
	}

	var body backfillBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "malformed request body", http.StatusBadRequest)
		return
	}
	if msg := validateBackfillRepos(body.Repositories); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Integration
		if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: s.namespace, Name: name}, &cur); err != nil {
			return err
		}
		if cur.Spec.Suspend {
			return &statusError{code: http.StatusConflict, msg: "Integration is suspended."}
		}
		if !backfillSupported(&cur) {
			return &statusError{code: http.StatusConflict,
				msg: fmt.Sprintf("Integration %q does not support backfill.", name)}
		}
		cur.Spec.Backfill = &v1alpha1.BackfillRequest{
			By:           id.Username,
			At:           metav1.NewTime(s.now()),
			Repositories: body.Repositories,
		}
		return s.client.Update(r.Context(), &cur)
	})
	if err != nil {
		var se *statusError
		if errors.As(err, &se) {
			http.Error(w, se.msg, se.code)
			return
		}
		if apierrors.IsNotFound(err) {
			http.Error(w, "integration not found", http.StatusNotFound)
			return
		}
		s.log.LogAttrs(r.Context(), slog.LevelError, "backfill request failed",
			slog.String("integration", name), slog.Any("error", err))
		http.Error(w, "action failed", http.StatusInternalServerError)
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "backfill requested",
		slog.String("integration", name), slog.String("user", id.Username),
		slog.Int("repositories", len(body.Repositories)))
	writeJSON(w, map[string]any{})
}

// validateBackfillRepos mirrors the CRD limits on the repository filter;
// empty means the credential's full scope.
func validateBackfillRepos(repos []string) string {
	if len(repos) > maxBackfillRepos {
		return fmt.Sprintf("at most %d repository filters", maxBackfillRepos)
	}
	for _, entry := range repos {
		if entry == "" || len(entry) > maxBackfillEntryLength {
			return fmt.Sprintf("repository filter entries must be 1-%d characters", maxBackfillEntryLength)
		}
		if strings.ContainsAny(entry, " \t\n") {
			return fmt.Sprintf("repository filter %q must not contain whitespace", entry)
		}
	}
	return ""
}
