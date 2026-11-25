// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/repoaccess"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

// The repositories page: a maintainer's own list of repositories (kept in
// their browser), with finding counts by phase for each, and, for a
// repository patchy has never seen, whether patchy can read its alerts and a
// one-repository backfill. "Never seen" means no finding and no repository
// FindingRollup. The backfill is refused for anything else, so the page can
// never become a way to hammer the GitHub API for repositories already
// flowing through the pipeline.

const (
	// maxSummaryRepos bounds GET /api/repositories: one pass over the
	// findings answers any number, so this only caps the response.
	maxSummaryRepos = 50
	// skippedKnown marks a repository the access check skipped because
	// patchy has already seen it.
	skippedKnown = "known"
)

// accessChecker asks the integration-controller whether repositories'
// alerts are readable; repoaccess.Client implements it.
type accessChecker interface {
	Enabled() bool
	Check(ctx context.Context, repos []string) (map[string]repoaccess.Result, error)
}

// WithRepositoryAccess wires the integration-controller's repository-access
// endpoint. Without it the repositories page still shows counts, and says
// the access check is unavailable.
func (s *Server) WithRepositoryAccess(c accessChecker) *Server {
	s.access = c
	return s
}

// RepositoriesPayload is the payload behind GET /api/repositories. It
// mirrors ui/src/types.ts.
type RepositoriesPayload struct {
	GeneratedAt  string              `json:"generatedAt"`
	Repositories []RepositorySummary `json:"repositories"`
	// CanBackfill reports whether the caller holds the backfill grant: the
	// page shows the button, or an "ask an operator" note.
	CanBackfill bool `json:"canBackfill"`
	// AccessCheck reports whether the integration-controller's access check
	// is configured.
	AccessCheck bool `json:"accessCheck"`
}

// RepositorySummary is one tracked repository's state.
type RepositorySummary struct {
	Name string `json:"name"`
	// Findings counts the repository's current findings; Phases splits them
	// by phase (a finding with no phase yet counts in Findings only).
	Findings int            `json:"findings"`
	Phases   map[string]int `json:"phases,omitempty"`
	// Rollup is the repository's all-time shard, when one exists.
	Rollup *RepositoryRollup `json:"rollup,omitempty"`
	// Backfill is the latest backfill request naming the repository.
	Backfill *RepositoryBackfill `json:"backfill,omitempty"`
}

// RepositoryRollup is the slice of a repository shard the page shows.
type RepositoryRollup struct {
	Findings      int64  `json:"findings"`
	LastProcessed string `json:"lastProcessed,omitempty"`
}

// RepositoryBackfill is a backfill request that covers the repository: an
// exact entry or an "owner/" prefix naming it, or a pending request with no
// filter at all.
type RepositoryBackfill struct {
	Integration string `json:"integration"`
	RequestedBy string `json:"requestedBy,omitempty"`
	RequestedAt string `json:"requestedAt,omitempty"`
	// Pending marks a request the controller has not attempted yet.
	Pending bool `json:"pending,omitempty"`
	// The outcome of the request's last run; the counts cover the whole
	// request, not this repository alone.
	LastRunAt string `json:"lastRunAt,omitempty"`
	Listed    int32  `json:"listed,omitempty"`
	Ingested  int32  `json:"ingested,omitempty"`
	Error     string `json:"error,omitempty"`
}

// RepositoryAccessPayload is the payload behind GET
// /api/repositories/access.
type RepositoryAccessPayload struct {
	// Available is false when no access check is configured.
	Available bool `json:"available"`
	// Error reports a failed check (the integration-controller unreachable,
	// say); the rows then show it rather than a verdict.
	Error        string             `json:"error,omitempty"`
	Repositories []RepositoryAccess `json:"repositories"`
}

// RepositoryAccess is one repository's access verdict.
type RepositoryAccess struct {
	Name string `json:"name"`
	// Skipped is "known" for a repository patchy has seen (findings or a
	// rollup), which is never probed.
	Skipped      string                         `json:"skipped,omitempty"`
	Integrations []repoaccess.IntegrationAccess `json:"integrations,omitempty"`
	Forge        *repoaccess.ForgeAccess        `json:"forge,omitempty"`
	// BackfillIntegration is the Integration a backfill would be stamped
	// on; empty when none can read the alerts.
	BackfillIntegration string `json:"backfillIntegration,omitempty"`
}

// repositoryBackfillBody is the POST /api/repositories/backfill payload.
type repositoryBackfillBody struct {
	Repository string `json:"repository"`
}

// RepositoryBackfillResult answers a one-repository backfill.
type RepositoryBackfillResult struct {
	Integration string `json:"integration"`
	// Outcome is "requested" (a new request), "merged" (added to a pending
	// request), or "covered" (a pending request already includes it).
	Outcome string `json:"outcome"`
}

// parseRepoList reads the repeated repo parameter: exact owner/name entries,
// deduplicated in order.
func parseRepoList(r *http.Request, limit int) ([]string, error) {
	vals := r.URL.Query()["repo"]
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if !repoaccess.ValidRepository(v) {
			return nil, fmt.Errorf("%w: repo %q is not an exact owner/name", errBadQuery, v)
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if len(out) > limit {
		return nil, fmt.Errorf("%w: at most %d repositories", errBadQuery, limit)
	}
	return out, nil
}

// handleTrackedRepositories serves GET /api/repositories behind the view
// gate.
func (s *Server) handleTrackedRepositories(w http.ResponseWriter, r *http.Request) {
	v, ok := s.authorize(w, r)
	if !ok {
		return
	}
	repos, err := parseRepoList(r, maxSummaryRepos)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out, err := s.buildRepositories(r.Context(), repos, v.grants)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "build repositories", slog.Any("error", err))
		http.Error(w, "failed to load repositories", http.StatusInternalServerError)
		return
	}
	writeJSONGzip(w, r, out)
}

func (s *Server) buildRepositories(
	ctx context.Context, repos []string, grants authz.Grants,
) (*RepositoriesPayload, error) {
	out := &RepositoriesPayload{
		GeneratedAt:  s.now().UTC().Format(time.RFC3339),
		Repositories: make([]RepositorySummary, 0, len(repos)),
		CanBackfill:  grants.AllowsIntegration(authz.VerbBackfill),
		AccessCheck:  s.access != nil && s.access.Enabled(),
	}
	if len(repos) == 0 {
		return out, nil
	}
	findings, err := s.listFindings(ctx)
	if err != nil {
		return nil, err
	}
	counts := countByRepoPhase(findings, repos)
	rollups, err := s.repositoryRollups(ctx, repos)
	if err != nil {
		return nil, err
	}
	var integs v1alpha1.IntegrationList
	if err := s.client.List(ctx, &integs, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("list integrations: %w", err)
	}
	for _, name := range repos {
		sum := RepositorySummary{Name: name}
		if c := counts[name]; c != nil {
			sum.Findings, sum.Phases = c.total, c.phases
		}
		if fr := rollups[name]; fr != nil {
			sum.Rollup = &RepositoryRollup{
				Findings:      fr.Status.Bucket.Findings,
				LastProcessed: stampPtr(fr.Status.LastProcessed),
			}
		}
		sum.Backfill = repositoryBackfill(integs.Items, name)
		out.Repositories = append(out.Repositories, sum)
	}
	return out, nil
}

// repositoryRollups returns the repository shards for repos, by key.
func (s *Server) repositoryRollups(ctx context.Context, repos []string) (map[string]*v1alpha1.FindingRollup, error) {
	items, err := s.listRollups(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]*v1alpha1.FindingRollup{}
	for i := range items {
		fr := &items[i]
		if fr.Spec.Scope.Type == v1alpha1.ScopeRepository && slices.Contains(repos, fr.Spec.Scope.Key) {
			out[fr.Spec.Scope.Key] = fr
		}
	}
	return out, nil
}

// backfillCovers reports whether a non-empty backfill filter names the
// repository, by the controller's own rule (ghclient.RepoMatches, mirrored
// here so the status server does not link the GitHub client): each entry is
// a case-insensitive prefix of "owner/name", and a slash-less entry is an
// owner. So "acme/shop" covers "acme/shop-api" too, exactly as the walk will.
func backfillCovers(filter []string, repo string) bool {
	full := strings.ToLower(repo)
	for _, entry := range filter {
		e := strings.ToLower(entry)
		if !strings.Contains(e, "/") {
			e += "/"
		}
		if strings.HasPrefix(full, e) {
			return true
		}
	}
	return false
}

// repositoryBackfill picks the backfill request that best describes the
// repository: a pending one over a finished one, then the newest. A filter
// that names the repository counts either way; an empty filter (the
// credential's whole scope) counts only while pending, since a finished
// whole-scope walk says nothing about whether this repository was in it.
func repositoryBackfill(integs []v1alpha1.Integration, repo string) *RepositoryBackfill {
	var best *RepositoryBackfill
	var bestAt time.Time
	for i := range integs {
		integ := &integs[i]
		req := integ.Spec.Backfill
		if req == nil || !backfillSupported(integ) {
			continue
		}
		wire := wireBackfill(integ)
		if !backfillCovers(req.Repositories, repo) && (len(req.Repositories) > 0 || !wire.Pending) {
			continue
		}
		cand := &RepositoryBackfill{
			Integration: integ.Name,
			RequestedBy: wire.RequestedBy,
			RequestedAt: wire.RequestedAt,
			Pending:     wire.Pending,
		}
		if !wire.Pending {
			cand.LastRunAt, cand.Listed, cand.Ingested, cand.Error = wire.LastRunAt, wire.Listed, wire.Ingested, wire.Error
		}
		better := best == nil ||
			(cand.Pending && !best.Pending) ||
			(cand.Pending == best.Pending && req.At.After(bestAt))
		if better {
			best, bestAt = cand, req.At.Time
		}
	}
	return best
}

// knownRepositories reports which of repos patchy has seen: any current
// finding, or a repository rollup.
func (s *Server) knownRepositories(ctx context.Context, repos []string) (map[string]bool, error) {
	findings, err := s.listFindings(ctx)
	if err != nil {
		return nil, err
	}
	counts := countByRepoPhase(findings, repos)
	rollups, err := s.repositoryRollups(ctx, repos)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, r := range repos {
		known[r] = counts[r] != nil || rollups[r] != nil
	}
	return known, nil
}

// handleRepositoryAccess serves GET /api/repositories/access behind the
// view gate. Only never-seen repositories are probed.
func (s *Server) handleRepositoryAccess(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r); !ok {
		return
	}
	repos, err := parseRepoList(r, repoaccess.MaxRepositories)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := RepositoryAccessPayload{Repositories: make([]RepositoryAccess, 0, len(repos))}
	if s.access == nil || !s.access.Enabled() {
		writeJSON(w, out)
		return
	}
	out.Available = true
	known, err := s.knownRepositories(r.Context(), repos)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "repository access", slog.Any("error", err))
		http.Error(w, "failed to load repositories", http.StatusInternalServerError)
		return
	}
	var unknown []string
	for _, name := range repos {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	var results map[string]repoaccess.Result
	if len(unknown) > 0 {
		results, err = s.access.Check(r.Context(), unknown)
		if err != nil {
			s.log.LogAttrs(r.Context(), slog.LevelWarn, "repository access check failed", slog.Any("error", err))
			out.Error = "The access check could not reach the integration-controller."
		}
	}
	for _, name := range repos {
		ra := RepositoryAccess{Name: name}
		if known[name] {
			ra.Skipped = skippedKnown
		} else if res, ok := results[name]; ok {
			ra.Integrations, ra.Forge = res.Integrations, res.Forge
			ra.BackfillIntegration, _ = res.Backfillable()
		}
		out.Repositories = append(out.Repositories, ra)
	}
	writeJSON(w, out)
}

// handleRepositoryBackfill serves POST /api/repositories/backfill: a
// backfill of one never-seen repository, stamped on the first Integration
// (by name) that can read its alerts.
func (s *Server) handleRepositoryBackfill(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorizeBackfill(w, r)
	if !ok {
		return
	}
	var body repositoryBackfillBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "malformed request body", http.StatusBadRequest)
		return
	}
	repo := body.Repository
	if !repoaccess.ValidRepository(repo) {
		http.Error(w, "repository must be an exact owner/name", http.StatusBadRequest)
		return
	}

	// The gate is enforced here, not just in the page: a repository with
	// findings or a rollup is already flowing through the pipeline.
	known, err := s.knownRepositories(r.Context(), []string{repo})
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "repository backfill", slog.Any("error", err))
		http.Error(w, "failed to load repositories", http.StatusInternalServerError)
		return
	}
	if known[repo] {
		http.Error(w, fmt.Sprintf("Patchy has already seen %s, so it needs no backfill.", repo), http.StatusConflict)
		return
	}
	if s.access == nil || !s.access.Enabled() {
		http.Error(w, "The repository access check is not configured, so a backfill cannot be targeted.",
			http.StatusServiceUnavailable)
		return
	}
	results, err := s.access.Check(r.Context(), []string{repo})
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "repository access check failed", slog.Any("error", err))
		http.Error(w, "The access check could not reach the integration-controller.", http.StatusBadGateway)
		return
	}
	res := results[repo]
	integration, ok := res.Backfillable()
	if !ok {
		http.Error(w, fmt.Sprintf("No integration can read the alerts of %s: %s", repo, accessSummary(res)),
			http.StatusConflict)
		return
	}

	outcome := "requested"
	err = s.stampBackfill(r.Context(), integration, func(cur *v1alpha1.Integration) error {
		req := cur.Spec.Backfill
		now := metav1.NewTime(s.now())
		if req == nil || !wireBackfill(cur).Pending {
			outcome = "requested"
			cur.Spec.Backfill = &v1alpha1.BackfillRequest{By: id.Username, At: now, Repositories: []string{repo}}
			return nil
		}
		if len(req.Repositories) == 0 || backfillCovers(req.Repositories, repo) {
			outcome = "covered"
			return errBackfillUnchanged
		}
		if len(req.Repositories) >= maxBackfillRepos {
			return &statusError{code: http.StatusConflict, msg: fmt.Sprintf(
				"The pending backfill on %q already names %d repositories. Try again once it has run.",
				integration, maxBackfillRepos)}
		}
		// Bump the request time: the controller echoes the time of the
		// request it walked, so a walk already in flight over the old list
		// leaves the merged request pending, and the next reconcile walks it.
		outcome = "merged"
		req.Repositories = append(req.Repositories, repo)
		req.At = now
		return nil
	})
	if err != nil {
		s.writeBackfillError(w, r, integration, err)
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "repository backfill requested",
		slog.String("integration", integration), slog.String("repository", repo),
		slog.String("user", id.Username), slog.String("outcome", outcome))
	writeJSON(w, RepositoryBackfillResult{Integration: integration, Outcome: outcome})
}

// accessSummary renders why no Integration can read a repository's alerts,
// for the 409 message.
func accessSummary(res repoaccess.Result) string {
	if len(res.Integrations) == 0 {
		return "no github integration ingests code or secret scanning alerts"
	}
	parts := make([]string, 0, len(res.Integrations))
	for _, ia := range res.Integrations {
		var checks []string
		for _, c := range []struct {
			name  string
			check *repoaccess.Check
		}{{"code scanning", ia.Alerts.CodeScanning}, {"secret scanning", ia.Alerts.SecretScanning}} {
			if c.check == nil {
				continue
			}
			s := c.name + " " + string(c.check.Status)
			if c.check.Detail != "" {
				s += " (" + c.check.Detail + ")"
			}
			checks = append(checks, s)
		}
		parts = append(parts, ia.Name+": "+strings.Join(checks, ", "))
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}
