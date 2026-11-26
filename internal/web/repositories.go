// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghuser"
	"github.com/bitwise-media-group/patchy/internal/repoaccess"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
	"github.com/bitwise-media-group/patchy/internal/web/ghlink"
)

// The repositories page: a maintainer's own list of repositories (kept in
// their browser, or imported from GitHub), with finding counts by phase for
// each, whether patchy can read a never-seen repository's alerts, and a
// one-repository backfill. A backfill runs at once for a holder of the
// backfill grant, or of backfill-maintained on a repository their linked
// GitHub identity maintains; anyone else may file a request an operator
// decides. Every path shares one cooldown per repository, so the page can
// never become a way to hammer the GitHub API.

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
	// page shows the button, or the request flow.
	CanBackfill bool `json:"canBackfill"`
	// CanBackfillMaintained reports the backfill-maintained grant: with a
	// linked GitHub identity, the button works on repositories the caller
	// maintains.
	CanBackfillMaintained bool `json:"canBackfillMaintained,omitempty"`
	// CanRequestBackfill reports that the caller may file a backfill request
	// for an operator to decide (anyone who can view findings).
	CanRequestBackfill bool `json:"canRequestBackfill"`
	// CooldownSeconds is how long after a repository's backfill ran that
	// another is refused.
	CooldownSeconds int64 `json:"cooldownSeconds"`
	// GitHub lists the Integrations the caller may link a GitHub account
	// through; absent when linking is off.
	GitHub []GitHubLink `json:"github,omitempty"`
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
	// BackfillAvailableAt is when the cooldown lets the repository be
	// backfilled again; absent when it may be now.
	BackfillAvailableAt string `json:"backfillAvailableAt,omitempty"`
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
	// Request names the BackfillRequest.
	Request     string `json:"request,omitempty"`
	Integration string `json:"integration"`
	RequestedBy string `json:"requestedBy,omitempty"`
	RequestedAt string `json:"requestedAt,omitempty"`
	// Phase is the request's phase (AwaitingApproval, Pending, Complete,
	// Failed, Denied); Via is how it was authorized.
	Phase string `json:"phase,omitempty"`
	Via   string `json:"via,omitempty"`
	// Pending marks an open request: not walked yet, or waiting for an
	// operator.
	Pending bool `json:"pending,omitempty"`
	// DecisionReason is an operator's note on a decided request.
	DecisionReason string `json:"decisionReason,omitempty"`
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
	// Request files a request for an operator to decide instead of running
	// the backfill on the caller's own grant.
	Request bool `json:"request,omitempty"`
}

// RepositoryBackfillResult answers a one-repository backfill.
type RepositoryBackfillResult struct {
	Integration string `json:"integration"`
	// Request names the BackfillRequest created or covering the repository.
	Request string `json:"request,omitempty"`
	// Outcome is "requested" (a new request that runs at once),
	// "awaiting-approval" (a new request an operator decides), or "covered"
	// (an open request already includes it).
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
	if err == nil {
		_, out.GitHub = s.userLinks(r, v.id)
	}
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
		GeneratedAt:           s.now().UTC().Format(time.RFC3339),
		Repositories:          make([]RepositorySummary, 0, len(repos)),
		CanBackfill:           grants.AllowsIntegration(authz.VerbBackfill),
		CanBackfillMaintained: grants.AllowsMaintained(authz.VerbBackfill) && s.githubEnabled(),
		CanRequestBackfill:    grants.View,
		CooldownSeconds:       int64(s.backfillCooldown().Seconds()),
		AccessCheck:           s.access != nil && s.access.Enabled(),
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
	reqs, err := s.listBackfillRequests(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
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
		sum.Backfill = repositoryBackfill(reqs, name)
		if until := cooldownUntil(reqs, name, s.backfillCooldown(), now); !until.IsZero() {
			sum.BackfillAvailableAt = until.UTC().Format(time.RFC3339)
		}
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

// repositoryBackfill picks the request that best describes the repository:
// an open one over a finished one, then the newest. A filter that names the
// repository counts either way; a whole-scope request counts only while
// open, since a finished whole-scope walk says nothing about whether this
// repository was in it. reqs is newest first.
func repositoryBackfill(reqs []v1alpha1.BackfillRequest, repo string) *RepositoryBackfill {
	var best *v1alpha1.BackfillRequest
	for i := range reqs {
		b := &reqs[i]
		open := backfillOpen(b)
		names := len(b.Spec.Repositories) > 0 && backfillCovers(b.Spec.Repositories, repo)
		if !names && (len(b.Spec.Repositories) > 0 || !open) {
			continue
		}
		if best == nil || (open && !backfillOpen(best)) {
			best = b
		}
		if open {
			break // newest open request wins
		}
	}
	if best == nil {
		return nil
	}
	w := wireBackfillRequest(best)
	out := &RepositoryBackfill{
		Request:     w.Name,
		Integration: w.Integration,
		RequestedBy: w.RequestedBy,
		RequestedAt: w.RequestedAt,
		Phase:       w.Phase,
		Via:         w.Via,
		Pending:     backfillOpen(best),
	}
	if d := best.Spec.Decision; d != nil {
		out.DecisionReason = d.Reason
	}
	if !out.Pending || best.Status.Attempts > 0 {
		out.LastRunAt, out.Listed, out.Ingested, out.Error = w.LastRunAt, w.Listed, w.Ingested, w.Error
	}
	return out
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
// backfill of one repository, by one of three paths. The backfill grant
// runs it at once on the first Integration (by name) that can read the
// alerts. backfill-maintained runs it at once when the caller's linked
// GitHub identity is admin or maintain on the repository. Anyone who can
// view findings may instead file a request an operator decides. Every path
// shares the open-request check and the cooldown.
func (s *Server) handleRepositoryBackfill(w http.ResponseWriter, r *http.Request) {
	id, grants, ok := s.identifyGrants(w, r)
	if !ok {
		return
	}
	if !grants.View {
		http.Error(w, fmt.Sprintf("Permission denied. User %q may not view findings in namespace %q.",
			id.Display(), s.namespace), http.StatusForbidden)
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

	via := v1alpha1.BackfillViaRequested
	switch {
	case body.Request:
	case grants.AllowsIntegration(authz.VerbBackfill):
		via = v1alpha1.BackfillViaGrant
	case grants.AllowsMaintained(authz.VerbBackfill):
		via = v1alpha1.BackfillViaMaintained
	default:
		http.Error(w, fmt.Sprintf("Permission denied. User %q may not backfill integrations in namespace %q. "+
			"Request a backfill instead, and an operator will decide it.", id.Display(), s.namespace),
			http.StatusForbidden)
		return
	}

	// One open request per repository, and one walk per cooldown window.
	reqs, err := s.listBackfillRequests(r.Context())
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "repository backfill", slog.Any("error", err))
		http.Error(w, "failed to load backfill requests", http.StatusInternalServerError)
		return
	}
	if b := openCovering(reqs, repo); b != nil {
		writeJSON(w, RepositoryBackfillResult{
			Integration: b.Spec.IntegrationRef.Name, Request: b.Name, Outcome: outcomeCovered,
		})
		return
	}
	now := s.now()
	if until := cooldownUntil(reqs, repo, s.backfillCooldown(), now); !until.IsZero() {
		w.Header().Set("Retry-After", fmt.Sprint(int(until.Sub(now).Seconds())+1))
		http.Error(w, fmt.Sprintf("Patchy backfilled %s %s ago; try again after %s.", repo,
			ago(until.Add(-s.backfillCooldown()), now), until.UTC().Format(time.RFC3339)),
			http.StatusTooManyRequests)
		return
	}
	if via == v1alpha1.BackfillViaRequested && openRequestsBy(reqs, id.Username) >= maxOpenRequestsPerUser {
		http.Error(w, fmt.Sprintf("You have %d backfill requests waiting for an operator already. "+
			"Try again once they are decided.", maxOpenRequestsPerUser), http.StatusTooManyRequests)
		return
	}

	nb := newBackfill{repos: []string{repo}, by: id.Username, via: via}
	var role string
	if via == v1alpha1.BackfillViaMaintained {
		integration, gh, se := s.maintainedBackfillTarget(w, r, *id, repo)
		if se != nil {
			http.Error(w, se.msg, se.code)
			return
		}
		nb.integration, nb.github, role = integration, gh, gh.Role
	} else {
		integration, se := s.backfillTarget(r, repo)
		if se != nil {
			http.Error(w, se.msg, se.code)
			return
		}
		nb.integration = integration
	}

	bfr, err := s.createBackfill(r.Context(), nb)
	if err != nil {
		s.writeBackfillError(w, r, nb.integration, err)
		return
	}
	outcome := outcomeRequested
	if via == v1alpha1.BackfillViaRequested {
		outcome = outcomeAwaitingApproval
	}
	attrs := []slog.Attr{
		slog.String("integration", nb.integration), slog.String("repository", repo),
		slog.String("request", bfr.Name), slog.String("user", id.Username),
		slog.String("via", string(via)), slog.String("outcome", outcome),
	}
	if nb.github != nil {
		attrs = append(attrs, slog.String("github.login", nb.github.Login), slog.String("github.role", role))
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "repository backfill requested", attrs...)
	writeJSON(w, RepositoryBackfillResult{Integration: nb.integration, Request: bfr.Name, Outcome: outcome})
}

// backfillTarget picks the Integration a granted or requested backfill is
// stamped on: the first, by name, that can read the repository's alerts.
func (s *Server) backfillTarget(r *http.Request, repo string) (string, *statusError) {
	if s.access == nil || !s.access.Enabled() {
		return "", &statusError{code: http.StatusServiceUnavailable,
			msg: "The repository access check is not configured, so a backfill cannot be targeted."}
	}
	results, err := s.access.Check(r.Context(), []string{repo})
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "repository access check failed", slog.Any("error", err))
		return "", &statusError{code: http.StatusBadGateway,
			msg: "The access check could not reach the integration-controller."}
	}
	res := results[repo]
	integration, ok := res.Backfillable()
	if !ok {
		return "", &statusError{code: http.StatusConflict,
			msg: fmt.Sprintf("No integration can read the alerts of %s: %s", repo, accessSummary(res))}
	}
	return integration, nil
}

// maintainedBackfillTarget picks the Integration a maintained backfill is
// stamped on: the first linked Integration, by name, through which the
// caller's GitHub role on the repository is admin or maintain and the
// alerts are readable. The 403 names why none qualified.
func (s *Server) maintainedBackfillTarget(
	w http.ResponseWriter, r *http.Request, id auth.Identity, repo string,
) (string, *v1alpha1.BackfillGitHubIdentity, *statusError) {
	if !s.githubEnabled() {
		return "", nil, &statusError{code: http.StatusForbidden,
			msg: "GitHub linking is not configured here, so maintainer backfills are unavailable. " +
				"Request a backfill instead."}
	}
	live, _ := s.userLinks(r, id)
	if len(live) == 0 {
		return "", nil, &statusError{code: http.StatusForbidden,
			msg: "Connect GitHub to backfill repositories you maintain, or request a backfill instead."}
	}
	var reasons []string
	for _, l := range live {
		var res *ghuser.AuthorizeRepositoryResponse
		err := s.links.Call(r.Context(), w, l.link, func(token string) error {
			var err error
			res, err = s.ghUsers.AuthorizeRepository(r.Context(), ghuser.AuthorizeRepositoryRequest{
				Integration: l.integ.Name, AccessToken: token, Repo: repo,
			})
			return err
		})
		switch {
		case errors.Is(err, ghlink.ErrNotLinked):
			reasons = append(reasons, l.integ.Name+": your GitHub connection expired; reconnect")
		case err != nil:
			s.log.LogAttrs(r.Context(), slog.LevelWarn, "github role check failed",
				slog.String("integration", l.integ.Name), slog.Any("error", err))
			reasons = append(reasons, l.integ.Name+": the GitHub role check failed")
		case !ghuser.Maintains(res.Role):
			reasons = append(reasons, fmt.Sprintf("%s: @%s's role is %s", l.integ.Name, l.link.Login, res.Role))
		case !res.Access.Readable():
			reasons = append(reasons, l.integ.Name+": patchy cannot read the alerts")
		default:
			return l.integ.Name, &v1alpha1.BackfillGitHubIdentity{Login: l.link.Login, Role: res.Role}, nil
		}
	}
	return "", nil, &statusError{code: http.StatusForbidden, msg: fmt.Sprintf(
		"You cannot backfill %s (it needs admin or maintain on GitHub): %s. Request a backfill instead.",
		repo, strings.Join(reasons, "; "))}
}

// ago renders a short elapsed time for messages: "less than a minute",
// "25m", "3h", "3h 5m".
func ago(t, now time.Time) string {
	d := now.Sub(t).Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case d < time.Minute:
		return "less than a minute"
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
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
