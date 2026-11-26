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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

// Backfill requests are their own objects (v1alpha1.BackfillRequest): the
// status page creates one per backfill and the integration-controller walks
// them. A request filed without a backfill grant waits for an operator's
// decision, which operators make from the same page.

const (
	// DefaultBackfillCooldown is how long after a one-repository backfill
	// ran that the repositories page refuses another for the same
	// repository.
	DefaultBackfillCooldown = 24 * time.Hour
	// maxOpenRequestsPerUser bounds the requests one user may have waiting
	// for a decision.
	maxOpenRequestsPerUser = 10
	// maxListedRequests caps GET /api/backfill-requests.
	maxListedRequests = 200
	// maxDecisionReason mirrors the CRD's limit on spec.decision.reason.
	maxDecisionReason = 500
	// backfillIntegrationLabel lets observers select an Integration's
	// requests.
	backfillIntegrationLabel = "patchy.bitwisemedia.uk/integration"
)

// Backfill outcomes a POST answers with.
const (
	// outcomeRequested: a new request that runs at once.
	outcomeRequested = "requested"
	// outcomeAwaitingApproval: a new request that waits for an operator.
	outcomeAwaitingApproval = "awaiting-approval"
	// outcomeCovered: an open request already covers the repository.
	outcomeCovered = "covered"
)

// WithBackfillCooldown sets the one-repository backfill cooldown; <= 0
// keeps the default.
func (s *Server) WithBackfillCooldown(d time.Duration) *Server {
	if d > 0 {
		s.cooldown = d
	}
	return s
}

func (s *Server) backfillCooldown() time.Duration {
	if s.cooldown <= 0 {
		return DefaultBackfillCooldown
	}
	return s.cooldown
}

// BackfillRequestWire is one BackfillRequest as the page renders it. It
// mirrors ui/src/types.ts.
type BackfillRequestWire struct {
	Name         string   `json:"name"`
	Integration  string   `json:"integration"`
	RequestedBy  string   `json:"requestedBy"`
	RequestedAt  string   `json:"requestedAt,omitempty"`
	Repositories []string `json:"repositories,omitempty"`
	// Via is backfill, maintained or requested.
	Via         string                `json:"via"`
	GitHubLogin string                `json:"githubLogin,omitempty"`
	Phase       string                `json:"phase,omitempty"`
	Attempts    int32                 `json:"attempts,omitempty"`
	LastRunAt   string                `json:"lastRunAt,omitempty"`
	CompletedAt string                `json:"completedAt,omitempty"`
	Listed      int32                 `json:"listed,omitempty"`
	Ingested    int32                 `json:"ingested,omitempty"`
	Truncated   bool                  `json:"truncated,omitempty"`
	Error       string                `json:"error,omitempty"`
	Decision    *BackfillDecisionWire `json:"decision,omitempty"`
}

// BackfillDecisionWire is an operator's verdict.
type BackfillDecisionWire struct {
	Verdict string `json:"verdict"`
	By      string `json:"by"`
	At      string `json:"at"`
	Reason  string `json:"reason,omitempty"`
}

// BackfillRequestsPayload is the payload behind GET /api/backfill-requests.
type BackfillRequestsPayload struct {
	GeneratedAt string                `json:"generatedAt"`
	Requests    []BackfillRequestWire `json:"requests"`
	// CanDecide reports the backfill grant: the caller may approve or deny
	// requests awaiting a decision.
	CanDecide bool `json:"canDecide"`
}

// backfillPhase is a request's effective phase: an undecided requested
// backfill the controller has not stamped yet still awaits approval.
func backfillPhase(b *v1alpha1.BackfillRequest) v1alpha1.BackfillRequestPhase {
	if b.Status.Phase != "" {
		return b.Status.Phase
	}
	if b.Spec.Via == v1alpha1.BackfillViaRequested && b.Spec.Decision == nil {
		return v1alpha1.BackfillAwaitingApproval
	}
	return v1alpha1.BackfillPending
}

// backfillVia is a request's authorization path; empty means the grant.
func backfillVia(b *v1alpha1.BackfillRequest) v1alpha1.BackfillVia {
	if b.Spec.Via == "" {
		return v1alpha1.BackfillViaGrant
	}
	return b.Spec.Via
}

// backfillOpen reports a request that has not finished: pending or awaiting
// a decision (an approved one not yet walked included).
func backfillOpen(b *v1alpha1.BackfillRequest) bool {
	return !backfillPhase(b).Terminal()
}

func wireBackfillRequest(b *v1alpha1.BackfillRequest) BackfillRequestWire {
	st := &b.Status
	out := BackfillRequestWire{
		Name:         b.Name,
		Integration:  b.Spec.IntegrationRef.Name,
		RequestedBy:  b.Spec.RequestedBy,
		RequestedAt:  stamp(b.CreationTimestamp),
		Repositories: b.Spec.Repositories,
		Via:          string(backfillVia(b)),
		Phase:        string(backfillPhase(b)),
		Attempts:     st.Attempts,
		LastRunAt:    stampPtr(st.LastAttemptAt),
		CompletedAt:  stampPtr(st.CompletedAt),
		Listed:       st.Listed,
		Ingested:     st.Ingested,
		Truncated:    st.Truncated,
		Error:        st.Error,
	}
	if gh := b.Spec.GitHub; gh != nil {
		out.GitHubLogin = gh.Login
	}
	if d := b.Spec.Decision; d != nil {
		out.Decision = &BackfillDecisionWire{Verdict: string(d.Verdict), By: d.By, At: stamp(d.At), Reason: d.Reason}
	}
	return out
}

// listBackfillRequests returns the namespace's requests, newest first.
func (s *Server) listBackfillRequests(ctx context.Context) ([]v1alpha1.BackfillRequest, error) {
	var list v1alpha1.BackfillRequestList
	if err := s.client.List(ctx, &list, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("list backfill requests: %w", err)
	}
	sort.SliceStable(list.Items, func(i, j int) bool {
		a, b := list.Items[i].CreationTimestamp.Time, list.Items[j].CreationTimestamp.Time
		if !a.Equal(b) {
			return a.After(b)
		}
		return list.Items[i].Name > list.Items[j].Name
	})
	return list.Items, nil
}

// latestBackfill is the newest request on the Integration, for the
// configuration view; requests still awaiting a decision are skipped (they
// are not the Integration's backfill yet).
func latestBackfill(reqs []v1alpha1.BackfillRequest, integration string) *v1alpha1.BackfillRequest {
	for i := range reqs {
		b := &reqs[i]
		if b.Spec.IntegrationRef.Name == integration && backfillPhase(b) != v1alpha1.BackfillAwaitingApproval {
			return b
		}
	}
	return nil
}

// cooldownUntil returns when the repository may be backfilled again from
// the repositories page: the latest walk that named it (by the walk's own
// prefix rule) and completed within the cooldown. Zero means now. A
// whole-scope walk says nothing about whether this repository was in it,
// so it never counts.
func cooldownUntil(reqs []v1alpha1.BackfillRequest, repo string, cooldown time.Duration, now time.Time) time.Time {
	var until time.Time
	for i := range reqs {
		b := &reqs[i]
		if backfillPhase(b) != v1alpha1.BackfillComplete || b.Status.CompletedAt == nil ||
			len(b.Spec.Repositories) == 0 || !backfillCovers(b.Spec.Repositories, repo) {
			continue
		}
		if t := b.Status.CompletedAt.Add(cooldown); t.After(now) && t.After(until) {
			until = t
		}
	}
	return until
}

// openCovering returns an open request that will walk the repository: one
// naming it, or a whole-scope one.
func openCovering(reqs []v1alpha1.BackfillRequest, repo string) *v1alpha1.BackfillRequest {
	for i := range reqs {
		b := &reqs[i]
		if !backfillOpen(b) {
			continue
		}
		if len(b.Spec.Repositories) == 0 || backfillCovers(b.Spec.Repositories, repo) {
			return b
		}
	}
	return nil
}

// openRequestsBy counts the requests a user has waiting for a decision.
func openRequestsBy(reqs []v1alpha1.BackfillRequest, user string) int {
	n := 0
	for i := range reqs {
		if reqs[i].Spec.RequestedBy == user && backfillPhase(&reqs[i]) == v1alpha1.BackfillAwaitingApproval {
			n++
		}
	}
	return n
}

// newBackfill describes one request to create.
type newBackfill struct {
	integration string
	repos       []string
	by          string
	via         v1alpha1.BackfillVia
	github      *v1alpha1.BackfillGitHubIdentity
}

// createBackfill checks the Integration every backfill path shares (exists,
// not suspended, supports backfill) and creates the request.
func (s *Server) createBackfill(ctx context.Context, nb newBackfill) (*v1alpha1.BackfillRequest, error) {
	var integ v1alpha1.Integration
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: nb.integration}, &integ); err != nil {
		return nil, err
	}
	if integ.Spec.Suspend {
		return nil, &statusError{code: http.StatusConflict, msg: "Integration is suspended."}
	}
	if !backfillSupported(&integ) {
		return nil, &statusError{code: http.StatusConflict,
			msg: fmt.Sprintf("Integration %q does not support backfill.", nb.integration)}
	}
	via := nb.via
	if via == v1alpha1.BackfillViaGrant {
		via = "" // the default; keeps direct and status-page requests alike
	}
	bfr := &v1alpha1.BackfillRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    s.namespace,
			GenerateName: "backfill-",
			Labels:       map[string]string{backfillIntegrationLabel: nb.integration},
		},
		Spec: v1alpha1.BackfillRequestSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: nb.integration},
			RequestedBy:    nb.by,
			Repositories:   nb.repos,
			Via:            via,
			GitHub:         nb.github,
		},
	}
	if err := s.client.Create(ctx, bfr); err != nil {
		return nil, err
	}
	return bfr, nil
}

// handleBackfillRequests serves GET /api/backfill-requests behind the view
// gate: every request, newest first, and whether the caller may decide.
func (s *Server) handleBackfillRequests(w http.ResponseWriter, r *http.Request) {
	v, ok := s.authorize(w, r)
	if !ok {
		return
	}
	reqs, err := s.listBackfillRequests(r.Context())
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "backfill requests", slog.Any("error", err))
		http.Error(w, "failed to load backfill requests", http.StatusInternalServerError)
		return
	}
	out := BackfillRequestsPayload{
		GeneratedAt: s.now().UTC().Format(time.RFC3339),
		Requests:    make([]BackfillRequestWire, 0, min(len(reqs), maxListedRequests)),
		CanDecide:   v.grants.AllowsIntegration(authz.VerbBackfill),
	}
	for i := range reqs {
		if len(out.Requests) >= maxListedRequests {
			break
		}
		out.Requests = append(out.Requests, wireBackfillRequest(&reqs[i]))
	}
	writeJSON(w, out)
}

// decisionBody is the POST /api/backfill-requests/{name}/{verdict} payload.
type decisionBody struct {
	Reason string `json:"reason"`
}

// handleBackfillDecision serves POST /api/backfill-requests/{name}/approve
// and /deny: an operator holding the backfill grant decides a requested
// backfill, once.
func (s *Server) handleBackfillDecision(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var verdict v1alpha1.BackfillVerdict
	switch r.PathValue("verdict") {
	case "approve":
		verdict = v1alpha1.BackfillApproved
	case "deny":
		verdict = v1alpha1.BackfillDeniedVerdict
	default:
		http.NotFound(w, r)
		return
	}
	id, grants, ok := s.identifyGrants(w, r)
	if !ok {
		return
	}
	if !grants.AllowsIntegration(authz.VerbBackfill) {
		http.Error(w, fmt.Sprintf("Permission denied. User %q may not decide backfill requests in namespace %q.",
			id.Display(), s.namespace), http.StatusForbidden)
		return
	}
	var body decisionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "malformed request body", http.StatusBadRequest)
		return
	}
	if len(body.Reason) > maxDecisionReason {
		http.Error(w, fmt.Sprintf("the reason may be at most %d characters", maxDecisionReason), http.StatusBadRequest)
		return
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.BackfillRequest
		if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: s.namespace, Name: name}, &cur); err != nil {
			return err
		}
		if backfillVia(&cur) != v1alpha1.BackfillViaRequested {
			return &statusError{code: http.StatusConflict, msg: "This backfill request needs no decision."}
		}
		if d := cur.Spec.Decision; d != nil {
			return &statusError{code: http.StatusConflict,
				msg: fmt.Sprintf("%s already %s this request.", d.By, verbPast(d.Verdict))}
		}
		cur.Spec.Decision = &v1alpha1.BackfillDecision{
			Verdict: verdict, By: id.Username, At: metav1.NewTime(s.now()), Reason: body.Reason,
		}
		return s.client.Update(r.Context(), &cur)
	})
	if err != nil {
		var se *statusError
		switch {
		case errors.As(err, &se):
			http.Error(w, se.msg, se.code)
		case apierrors.IsNotFound(err):
			http.Error(w, "backfill request not found", http.StatusNotFound)
		default:
			s.log.LogAttrs(r.Context(), slog.LevelError, "backfill decision failed",
				slog.String("request", name), slog.Any("error", err))
			http.Error(w, "action failed", http.StatusInternalServerError)
		}
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "backfill request decided",
		slog.String("request", name), slog.String("verdict", string(verdict)), slog.String("user", id.Username))
	writeJSON(w, map[string]any{})
}

func verbPast(v v1alpha1.BackfillVerdict) string {
	if v == v1alpha1.BackfillApproved {
		return "approved"
	}
	return "denied"
}

// identifyGrants identifies the caller and resolves their grants, writing
// the failure response itself; ok is false when it has.
func (s *Server) identifyGrants(w http.ResponseWriter, r *http.Request) (*auth.Identity, authz.Grants, bool) {
	id, err := s.auth.Identify(w, r)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "identify failed", slog.Any("error", err))
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return nil, authz.Grants{}, false
	}
	if id == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return nil, authz.Grants{}, false
	}
	grants, err := s.granter.Grants(r.Context(), *id)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "grants failed", slog.Any("error", err))
		http.Error(w, "authorization failed", http.StatusInternalServerError)
		return nil, authz.Grants{}, false
	}
	return id, grants, true
}
