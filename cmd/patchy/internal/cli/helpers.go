// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
)

// now is the clock, indirected so tests can pin it.
var now = time.Now

// listOptions builds the list options for the environment's scope.
func listOptions(env *kubecfg.Env, selector string) []client.ListOption {
	var out []client.ListOption
	if env.Namespace != "" {
		out = append(out, client.InNamespace(env.Namespace))
	}
	// A selector that will not parse is dropped rather than fatal: the same
	// expression is already sent to the API server on the table path, which
	// reports the syntax error with far better context than this would.
	if selector != "" {
		if sel, err := labels.Parse(selector); err == nil {
			out = append(out, client.MatchingLabelsSelector{Selector: sel})
		}
	}
	return out
}

// objectKey names an object in the environment's namespace.
func objectKey(env *kubecfg.Env, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: env.Namespace, Name: name}
}

// extractItems pulls the items out of any list type.
func extractItems(list client.ObjectList) []client.Object {
	items, err := meta.ExtractList(list)
	if err != nil {
		return nil
	}
	out := make([]client.Object, 0, len(items))
	for _, item := range items {
		if obj, ok := item.(client.Object); ok {
			out = append(out, obj)
		}
	}
	return out
}

// containsFold reports case-insensitive membership.
func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

// phaseNames lists the Finding phases, for completion and validation.
func phaseNames() []string {
	return []string{
		string(v1alpha1.PhaseOpened), string(v1alpha1.PhaseEnhanced),
		string(v1alpha1.PhaseInvestigating), string(v1alpha1.PhaseQueued),
		string(v1alpha1.PhaseAwaitingApproval), string(v1alpha1.PhaseRemediating),
		string(v1alpha1.PhaseInReview), string(v1alpha1.PhaseRemediated),
		string(v1alpha1.PhaseFailed), string(v1alpha1.PhaseDismissed),
		string(v1alpha1.PhaseHandedOff),
	}
}

// levelNames lists the severity/priority vocabulary, lowest first.
func levelNames() []string {
	return []string{
		string(v1alpha1.LevelLow), string(v1alpha1.LevelMedium),
		string(v1alpha1.LevelHigh), string(v1alpha1.LevelCritical),
	}
}

// runName builds the deterministic child name for one attempt. The controllers
// mint these names (investigation gate, remediation spawner), so the CLI can
// address an attempt directly instead of listing and filtering.
func runName(finding, kind string, attempt int32) string {
	suffix := "inv"
	if kind == "remediation" {
		suffix = "rem"
	}
	return fmt.Sprintf("%s-%s-%d", finding, suffix, attempt)
}

// notef writes a human-facing line. Output is narration or a result summary,
// never the payload a caller parses, so a failed write here is not worth
// aborting the run over — and a closed stdout (a `| head` that exited) is the
// usual cause.
func notef(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
