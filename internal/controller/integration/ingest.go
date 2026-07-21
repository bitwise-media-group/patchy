// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// maxAlerts caps spec.alerts; later alerts only bump overflowAlerts.
const maxAlerts = 64

// DefaultWindow is the accumulation window when unconfigured.
const DefaultWindow = time.Hour

// Ingestor folds scanner findings into Finding resources. The deterministic
// name plus AlreadyExists-tolerant create is the idempotency mechanism — no
// in-process mutex, the API server serializes.
type Ingestor struct {
	client.Client
	// Namespace the Findings live in.
	Namespace string
	// Window is the accumulation window (default DefaultWindow).
	Window time.Duration
	// Now is the clock seam; nil means time.Now.
	Now func() time.Time
	// Log receives ingest diagnostics; nil discards.
	Log *slog.Logger
}

// keyHash is the hex form of the accumulation key's hash — the label value
// selecting a finding family across generations.
func keyHash(integration, sourceID, repoURL, advisory string) string {
	sum := sha256.Sum256([]byte(integration + "|" + sourceID + "|" + repoURL + "|" + advisory))
	return hex.EncodeToString(sum[:5])
}

// Ingest folds one scanner finding into the cluster: append its alert to the
// live pre-investigation Finding of its family, or create the next
// generation.
func (in *Ingestor) Ingest(ctx context.Context, integ *v1alpha1.Integration, f source.Finding) error {
	repoURL := "https://" + githubHost(integ) + "/" + f.Repo.Owner + "/" + f.Repo.Name
	primary := ""
	if len(f.Advisories) > 0 {
		primary = f.Advisories[0]
	}
	hash := keyHash(integ.Name, f.Source, repoURL, primary)

	var family v1alpha1.FindingList
	if err := in.List(ctx, &family, client.InNamespace(in.Namespace),
		client.MatchingLabels{v1alpha1.LabelKeyHash: hash}); err != nil {
		return fmt.Errorf("list finding family %s: %w", hash, err)
	}

	// Fold into a live pre-investigation generation when one exists.
	maxGen := 0
	for i := range family.Items {
		cur := &family.Items[i]
		if gen := generationOf(cur.Name); gen > maxGen {
			maxGen = gen
		}
		if cur.DeletionTimestamp.IsZero() && foldable(cur.Status.Phase) {
			err := in.fold(ctx, cur.Name, f)
			if err == errRaced {
				// The live generation advanced mid-fold; open its successor.
				gen := generationOf(cur.Name)
				return in.create(ctx, integ, f, repoURL, hash, gen+1, cur.Name)
			}
			return err
		}
	}

	return in.create(ctx, integ, f, repoURL, hash, maxGen+1, prevName(family.Items, maxGen))
}

// errRaced reports a fold target that left the foldable phases mid-fold.
var errRaced = fmt.Errorf("finding advanced past accumulation")

// foldable phases still accept new alerts: the accumulation window overlaps
// enhancement, and an aged window only closes via the AccumulationComplete
// condition, not the phase.
func foldable(p v1alpha1.Phase) bool {
	return p == v1alpha1.PhaseOpened || p == v1alpha1.PhaseEnhanced
}

// generationOf parses the trailing generation ordinal of a Finding name.
func generationOf(name string) int {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '-' {
			n, err := strconv.Atoi(name[i+1:])
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

// prevName returns the name of the generation maxGen, for the successor
// edge; empty when none.
func prevName(items []v1alpha1.Finding, maxGen int) string {
	for i := range items {
		if generationOf(items[i].Name) == maxGen {
			return items[i].Name
		}
	}
	return ""
}

// fold appends the finding's alert to an existing Finding, idempotent on
// alert ID, under conflict retry.
func (in *Ingestor) fold(ctx context.Context, name string, f source.Finding) error {
	alert := toAlert(f)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := in.Get(ctx, types.NamespacedName{Namespace: in.Namespace, Name: name}, &cur); err != nil {
			return err
		}
		if !foldable(cur.Status.Phase) {
			return errRaced
		}
		if slices.ContainsFunc(cur.Spec.Alerts, func(a v1alpha1.Alert) bool { return a.ID == alert.ID }) {
			return nil
		}
		if len(cur.Spec.Alerts) >= maxAlerts {
			cur.Spec.OverflowAlerts++
		} else {
			cur.Spec.Alerts = append(cur.Spec.Alerts, alert)
		}
		// New advisories fold in too (same primary, richer identifiers).
		for _, adv := range f.Advisories {
			if !slices.Contains(cur.Spec.Advisories, adv) {
				cur.Spec.Advisories = append(cur.Spec.Advisories, adv)
			}
		}
		if err := in.Update(ctx, &cur); err != nil {
			return err
		}
		in.log().LogAttrs(ctx, slog.LevelInfo, "alert folded into finding",
			slog.String("finding", cur.Name), slog.String("alert", alert.ID))
		return nil
	})
}

// create makes generation gen of the family, records the successor edge, and
// opens the accumulation window.
func (in *Ingestor) create(
	ctx context.Context, integ *v1alpha1.Integration, f source.Finding,
	repoURL, hash string, gen int, prev string,
) error {
	name := fmt.Sprintf("finding-%s-%d", hash, gen)
	fnd := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: in.Namespace,
			Labels: map[string]string{
				v1alpha1.LabelKeyHash:     hash,
				v1alpha1.LabelSource:      f.Source,
				v1alpha1.LabelIntegration: integ.Name,
				v1alpha1.LabelRepoHash:    hashOf(repoURL),
				v1alpha1.LabelSeverity:    string(levelOf(f.Severity)),
			},
		},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: integ.Name},
			TrackingRef:    trackingRef(integ),
			Source:         f.Source,
			Repository: &v1alpha1.FindingRepository{
				Type: v1alpha1.RepositoryTypeGitHub,
				URL:  repoURL,
				Name: f.Repo.String(),
			},
			Advisories:  f.Advisories,
			RuleID:      f.RuleID,
			Title:       f.Title,
			Description: truncate(f.Description, 65536),
			Severity:    levelOf(f.Severity),
			Alerts:      []v1alpha1.Alert{toAlert(f)},
		},
	}
	if prev != "" {
		fnd.Spec.Related = []v1alpha1.RelatedFinding{{
			From: name, To: prev, Relationship: v1alpha1.RelationshipSuccessorOf,
		}}
	}
	if err := in.Create(ctx, fnd); err != nil {
		if kerrors.IsAlreadyExists(err) {
			// Two deliveries raced; the winner's object is the family live
			// generation — fold into it.
			return in.fold(ctx, name, f)
		}
		return fmt.Errorf("create finding %s: %w", name, err)
	}

	now := in.now()
	t := metav1.NewTime(now)
	until := metav1.NewTime(now.Add(in.window()))
	if err := v1alpha1.SetPhase(fnd, v1alpha1.PhaseOpened, now); err != nil {
		return err
	}
	fnd.Status.FirstObservedAt = &t
	fnd.Status.AccumulateUntil = &until
	if err := in.Status().Update(ctx, fnd); err != nil {
		// The projection reconciler backfills window fields for a bare
		// Opened-less Finding; log and let it.
		in.log().LogAttrs(ctx, slog.LevelWarn, "finding status init failed",
			slog.String("finding", name), slog.Any("error", err))
	}

	// Mirror the successor edge onto the elder, best-effort.
	if prev != "" {
		in.mirrorEdge(ctx, prev, fnd.Spec.Related[0])
	}
	in.log().LogAttrs(ctx, slog.LevelInfo, "finding created",
		slog.String("finding", name), slog.String("repo", f.Repo.String()))
	return nil
}

// mirrorEdge appends the successor edge to the elder generation's spec,
// best-effort under conflict retry.
func (in *Ingestor) mirrorEdge(ctx context.Context, elder string, edge v1alpha1.RelatedFinding) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := in.Get(ctx, types.NamespacedName{Namespace: in.Namespace, Name: elder}, &cur); err != nil {
			return err
		}
		if slices.Contains(cur.Spec.Related, edge) {
			return nil
		}
		if len(cur.Spec.Related) >= 32 {
			return nil
		}
		cur.Spec.Related = append(cur.Spec.Related, edge)
		return in.Update(ctx, &cur)
	})
	if err != nil {
		in.log().LogAttrs(ctx, slog.LevelWarn, "successor edge mirror failed",
			slog.String("elder", elder), slog.Any("error", err))
	}
}

// toAlert maps a scanner finding's alert fields.
func toAlert(f source.Finding) v1alpha1.Alert {
	a := v1alpha1.Alert{ID: strconv.Itoa(f.AlertNumber), URL: f.HTMLURL}
	for i, loc := range f.Locations {
		if i == 8 {
			break
		}
		a.Locations = append(a.Locations, v1alpha1.Location{
			Path:      loc.Path,
			StartLine: int32(loc.StartLine),
			EndLine:   int32(loc.EndLine),
			Snippet:   truncate(loc.Snippet, 1024),
		})
	}
	return a
}

// trackingRef denormalizes the projecting integration at creation: the
// ingesting integration itself when issues-enabled, else the namespace's
// issues-enabled one (nil when none — the finding is still tracked in-cluster).
func trackingRef(integ *v1alpha1.Integration) *v1alpha1.LocalObjectReference {
	if issuesEnabled(integ) {
		return &v1alpha1.LocalObjectReference{Name: integ.Name}
	}
	return nil
}

func (in *Ingestor) window() time.Duration {
	if in.Window <= 0 {
		return DefaultWindow
	}
	return in.Window
}

func (in *Ingestor) now() time.Time {
	if in.Now == nil {
		return time.Now()
	}
	return in.Now()
}

func (in *Ingestor) log() *slog.Logger {
	if in.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return in.Log
}

// hashOf is the label-value hash of an arbitrary string (repo URLs don't fit
// label values).
func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:5])
}

// truncate caps s at limit bytes without splitting a rune (the API server
// rejects invalid UTF-8).
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// levelOf maps a scanner severity onto the Level enum; unknown values are
// dropped (the field is optional and enum-validated).
func levelOf(s string) v1alpha1.Level {
	switch l := v1alpha1.Level(s); l {
	case v1alpha1.LevelLow, v1alpha1.LevelMedium, v1alpha1.LevelHigh, v1alpha1.LevelCritical:
		return l
	default:
		return ""
	}
}
