// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evalapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	ctrleval "github.com/bitwise-media-group/patchy/internal/controller/evaluation"
	"github.com/bitwise-media-group/patchy/pkg/evaluation"
)

// maxExecJSONBytes mirrors the CRD's UnitPlan.ExecJSON MaxLength.
const maxExecJSONBytes = 131072

// handleSubmit validates a Submission and creates the Evaluation.
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r, VerbCreate)
	if !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, s.limits.MaxSubmissionBytes)
	var sub evaluation.Submission
	if err := json.NewDecoder(body).Decode(&sub); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "submission exceeds the configured cap")
			return
		}
		writeError(w, http.StatusBadRequest, "malformed submission: "+err.Error())
		return
	}

	eval, missing, err := s.buildEvaluation(&sub, id.Username)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(missing) > 0 {
		// Confirm against the cache before failing — the client may race its
		// own uploads.
		still, err := s.confirmMissing(r, missing)
		if err != nil {
			writeError(w, http.StatusBadGateway, "workspace cache unavailable")
			return
		}
		if len(still) > 0 {
			writeJSON(w, http.StatusPreconditionFailed, evaluation.SubmissionError{
				Error:             "workspaces not uploaded",
				MissingWorkspaces: still,
			})
			return
		}
	}

	if raw, err := json.Marshal(eval); err != nil {
		writeError(w, http.StatusInternalServerError, "marshal evaluation")
		return
	} else if len(raw) > s.limits.MaxEvaluationBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("submission renders to %d bytes (cap %d); trim prior entries or split the batch",
				len(raw), s.limits.MaxEvaluationBytes))
		return
	}

	if err := s.client.Create(r.Context(), eval); err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "create evaluation", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "creating the evaluation failed")
		return
	}

	units := make([]string, len(eval.Spec.Units))
	for i := range units {
		units[i] = ctrleval.UnitName(eval.Name, i)
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "evaluation submitted",
		slog.String("evaluation", eval.Name),
		slog.String("submitter", id.Username),
		slog.Int("units", len(units)))
	writeJSON(w, http.StatusCreated, evaluation.SubmissionResponse{Name: eval.Name, Units: units})
}

// buildEvaluation validates the submission and renders the CR; it returns
// the distinct workspace digests, later confirmed against the cache.
func (s *Server) buildEvaluation(sub *evaluation.Submission, submitter string) (*v1alpha1.Evaluation, []string, error) {
	if sub.Version != evaluation.SubmissionVersion {
		return nil, nil, fmt.Errorf("unsupported submission version %q (want %s)", sub.Version, evaluation.SubmissionVersion)
	}
	if len(sub.Units) == 0 {
		return nil, nil, errors.New("submission has no units")
	}
	if len(sub.Units) > s.limits.MaxUnits {
		return nil, nil, fmt.Errorf("submission has %d units (cap %d)", len(sub.Units), s.limits.MaxUnits)
	}

	var digests []string
	plans := make([]v1alpha1.UnitPlan, 0, len(sub.Units))
	for i, u := range sub.Units {
		plan, err := unitPlanFor(u)
		if err != nil {
			return nil, nil, fmt.Errorf("unit %d: %w", i, err)
		}
		plans = append(plans, plan)
		if !slices.Contains(digests, u.Workspace.Digest) {
			digests = append(digests, u.Workspace.Digest)
		}
	}

	eval := &v1alpha1.Evaluation{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "eval-",
			Namespace:    s.namespace,
		},
		Spec: v1alpha1.EvaluationSpec{
			Submitter: submitter,
			Units:     plans,
		},
	}
	if sub.TTLSeconds > 0 {
		ttl := int32(min(sub.TTLSeconds, int64(1<<31-1)))
		eval.Spec.TTLSecondsAfterFinished = &ttl
	}
	return eval, digests, nil
}

// unitPlanFor validates one UnitSpec and renders its CR plan: the fields
// patchy schedules on typed, everything the pod needs serialized whole into
// ExecJSON.
func unitPlanFor(u evaluation.UnitSpec) (v1alpha1.UnitPlan, error) {
	switch {
	case u.Skill == "":
		return v1alpha1.UnitPlan{}, errors.New("skill is required")
	case u.Model == "":
		return v1alpha1.UnitPlan{}, errors.New("model is required")
	case u.Tier != 1 && u.Tier != 2:
		return v1alpha1.UnitPlan{}, fmt.Errorf("tier %d is not 1 or 2", u.Tier)
	case len(u.Harnesses) == 0:
		return v1alpha1.UnitPlan{}, errors.New("at least one harness option is required")
	case len(u.Harnesses) > 8:
		return v1alpha1.UnitPlan{}, fmt.Errorf("%d harness options (cap 8)", len(u.Harnesses))
	case !validDigest(u.Workspace.Digest):
		return v1alpha1.UnitPlan{}, fmt.Errorf("malformed workspace digest %q", u.Workspace.Digest)
	}

	execJSON, err := json.Marshal(u)
	if err != nil {
		return v1alpha1.UnitPlan{}, fmt.Errorf("serialize unit: %w", err)
	}
	if len(execJSON) > maxExecJSONBytes {
		return v1alpha1.UnitPlan{}, fmt.Errorf(
			"unit serializes to %d bytes (cap %d); trim the prior entry", len(execJSON), maxExecJSONBytes)
	}

	harnesses := make([]v1alpha1.HarnessOption, 0, len(u.Harnesses))
	for _, h := range u.Harnesses {
		if h.Harness == "" {
			return v1alpha1.UnitPlan{}, errors.New("harness option with empty harness id")
		}
		harnesses = append(harnesses, v1alpha1.HarnessOption{Harness: h.Harness, ModelID: h.ModelID})
	}
	plan := v1alpha1.UnitPlan{
		Skill:               u.Skill,
		Tier:                int32(u.Tier),
		Model:               u.Model,
		Harnesses:           harnesses,
		Workspace:           v1alpha1.WorkspaceRef{Digest: u.Workspace.Digest, SizeBytes: u.Workspace.SizeBytes},
		TimeoutMilliseconds: u.TimeoutMS,
		MaxTurns:            int32(u.MaxTurns),
		RunsPerQuery:        int32(u.RunsPerQuery),
		ExecJSON:            string(execJSON),
	}
	if u.Judge != nil {
		plan.JudgeModel = u.Judge.Model
	}
	return plan, nil
}

// confirmMissing re-checks digests against the cache and returns those still
// absent.
func (s *Server) confirmMissing(r *http.Request, digests []string) ([]string, error) {
	var missing []string
	for _, d := range digests {
		cached, err := s.workspaces.Stat(r.Context(), d)
		if err != nil {
			return nil, err
		}
		if !cached {
			missing = append(missing, d)
		}
	}
	return missing, nil
}

// handleCancel deletes the evaluation; children, Jobs, and results cascade.
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r, VerbDelete); !ok {
		return
	}
	name := r.PathValue("name")
	var eval v1alpha1.Evaluation
	if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: s.namespace, Name: name}, &eval); err != nil {
		if kerrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "no such evaluation")
			return
		}
		writeError(w, http.StatusInternalServerError, "reading the evaluation failed")
		return
	}
	if err := s.client.Delete(r.Context(), &eval); err != nil && !kerrors.IsNotFound(err) {
		writeError(w, http.StatusInternalServerError, "deleting the evaluation failed")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
