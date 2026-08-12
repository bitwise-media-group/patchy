// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evalapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strconv"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/evalresults"
	"github.com/bitwise-media-group/patchy/pkg/evaluation"
)

// handleSnapshot serves the point-in-time EvaluationStatusWire, unit results
// lifted from their ConfigMaps.
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r, VerbGet); !ok {
		return
	}
	name := r.PathValue("name")
	snap, found, err := s.snapshot(r.Context(), name, true)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "snapshot evaluation",
			slog.String("evaluation", name), slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "reading the evaluation failed")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "no such evaluation")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// snapshot assembles the wire view of one evaluation. withResults lifts
// settled units' entries out of their ConfigMaps.
func (s *Server) snapshot(ctx context.Context, name string,
	withResults bool) (*evaluation.EvaluationStatusWire, bool, error) {
	var eval v1alpha1.Evaluation
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: s.namespace, Name: name}, &eval); err != nil {
		if kerrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	units, err := s.listUnits(ctx, name)
	if err != nil {
		return nil, false, err
	}

	snap := &evaluation.EvaluationStatusWire{
		Name:          eval.Name,
		Phase:         string(eval.Status.Phase),
		Submitter:     eval.Spec.Submitter,
		UnitsTotal:    int(eval.Status.Units),
		UnitsComplete: int(eval.Status.UnitsComplete),
		UnitsFailed:   int(eval.Status.UnitsFailed),
	}
	if snap.UnitsTotal == 0 {
		snap.UnitsTotal = len(eval.Spec.Units)
	}
	for i := range units {
		snap.Units = append(snap.Units, s.unitWire(ctx, &units[i], withResults))
	}
	return snap, true, nil
}

// listUnits returns the evaluation's children, index-ordered.
func (s *Server) listUnits(ctx context.Context, evalName string) ([]v1alpha1.EvaluationUnit, error) {
	var list v1alpha1.EvaluationUnitList
	if err := s.client.List(ctx, &list, client.InNamespace(s.namespace),
		client.MatchingLabels{v1alpha1.LabelEvaluation: evalName}); err != nil {
		return nil, err
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Spec.Index < list.Items[j].Spec.Index
	})
	return list.Items, nil
}

// unitWire renders one unit; withResult lifts the entry from its ConfigMap
// for settled units.
func (s *Server) unitWire(ctx context.Context, unit *v1alpha1.EvaluationUnit,
	withResult bool) evaluation.UnitStatusWire {
	wire := evaluation.UnitStatusWire{
		Name:    unit.Name,
		Index:   int(unit.Spec.Index),
		Phase:   string(unit.Status.Phase),
		Harness: unit.Status.Harness,
		Reason:  string(unit.Status.Reason),
		Detail:  unit.Status.Detail,
	}
	if !settled(unit) {
		return wire
	}

	sum := summaryFromStatus(&unit.Status)
	wire.Summary = &sum
	if !withResult || unit.Status.Phase != v1alpha1.RunComplete {
		return wire
	}

	result := &evaluation.UnitResult{
		Tier:    int(unit.Spec.Unit.Tier),
		Model:   unit.Spec.Unit.Model,
		Harness: unit.Status.Harness,
		Failed:  unit.Status.CasesFailed > 0 || unit.Status.CasesErrored > 0,
		Summary: sum,
	}
	if ref := unit.Status.ResultsRef; ref != nil {
		entry, err := evalresults.Load(ctx, s.client, unit.Namespace, ref.Name)
		if err != nil {
			s.log.LogAttrs(ctx, slog.LevelWarn, "load unit results",
				slog.String("unit", unit.Name), slog.Any("error", err))
		} else {
			result.Entry = json.RawMessage(entry)
		}
	}
	wire.Result = result
	return wire
}

// settled reports whether the unit reached a terminal phase.
func settled(unit *v1alpha1.EvaluationUnit) bool {
	return unit.Status.Phase == v1alpha1.RunComplete || unit.Status.Phase == v1alpha1.RunFailed
}

// summaryFromStatus reconstructs the wire summary from the stamped status.
func summaryFromStatus(st *v1alpha1.EvaluationUnitStatus) evaluation.ResultSummary {
	sum := evaluation.ResultSummary{
		CasesPassed:  int(st.CasesPassed),
		CasesFailed:  int(st.CasesFailed),
		CasesErrored: int(st.CasesErrored),
		ElapsedMS:    st.ElapsedMilliseconds,
		Outcome:      "ok",
	}
	if st.Phase == v1alpha1.RunFailed {
		sum.Outcome = string(st.Reason)
	}
	for _, c := range st.Cases {
		sum.Cases = append(sum.Cases, evaluation.CaseStatus{ID: c.ID, Passed: c.Passed})
	}
	sum.TokenUsage = evaluation.TokenUsage{
		InputTokens:         st.Usage.InputTokens,
		OutputTokens:        st.Usage.OutputTokens,
		CacheReadTokens:     st.Usage.CacheReadTokens,
		CacheCreationTokens: st.Usage.CacheCreationTokens,
	}
	if st.Usage.CostUSD != "" {
		if f, err := strconv.ParseFloat(st.Usage.CostUSD, 64); err == nil {
			sum.TokenUsage.CostUSD = f
		}
	}
	return sum
}
