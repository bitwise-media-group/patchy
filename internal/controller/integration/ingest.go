// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// maxAlerts caps spec.alerts; later alerts only bump overflowAlerts.
const maxAlerts = 64

// DefaultWindow is the accumulation window when unconfigured.
const DefaultWindow = time.Hour

// KeyHashIndex is the field index over the key-hash label. Every ingested
// alert selects its finding family by this value; without the index that
// List is a full informer-store scan per alert — O(backlog), ruinous on a
// brownfield estate — where the index lookup is O(1).
const KeyHashIndex = "labels.key-hash"

// KeyHashIndexer extracts the index value: the finding's key-hash label.
// Exported so fake-client tests register the same recipe the manager does.
func KeyHashIndexer(obj client.Object) []string {
	hash := obj.GetLabels()[v1alpha1.LabelKeyHash]
	if hash == "" {
		return nil
	}
	return []string{hash}
}

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
//
// scope is what the finding is raised against: the repository URL for a code
// finding, the cloud resource name for an infrastructure one. It occupies the
// position the repository URL always has, and MUST keep doing so: the hash is
// persisted in a label on every live Finding, so changing the string for a
// repo-bearing finding orphans every existing family. Accumulation would then
// find nothing, open generation 1 alongside the live one, and project a
// second tracking issue for every open finding in the estate. There is no
// migration — the label is frozen by the admission policy.
func keyHash(integration, sourceID, scope, advisory string) string {
	sum := sha256.Sum256([]byte(integration + "|" + sourceID + "|" + scope + "|" + advisory))
	return hex.EncodeToString(sum[:5])
}

// SetupWithManager registers the key-hash field index on the manager's
// cache. The Ingestor is webhook-driven — it runs no reconciler — so this is
// its only manager hook; without it every family List errors, which is the
// loud failure mode we want over a silent full scan.
func (in *Ingestor) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &v1alpha1.Finding{}, KeyHashIndex, KeyHashIndexer); err != nil {
		return fmt.Errorf("index %s: %w", KeyHashIndex, err)
	}
	return nil
}

// Ingest folds one scanner finding into the cluster: append its alert to the
// live pre-investigation Finding of its family, or create the next
// generation.
func (in *Ingestor) Ingest(ctx context.Context, integ *v1alpha1.Integration, f source.Finding) error {
	repoURL := repositoryURL(integ, f)
	scope := accumulationScope(repoURL, f)
	if scope == "" {
		return fmt.Errorf("ingest %s finding: names neither a repository nor a cloud resource", f.Source)
	}
	primary := ""
	if len(f.Advisories) > 0 {
		primary = f.Advisories[0]
	}
	hash := keyHash(integ.Name, f.Source, scope, primary)

	var family v1alpha1.FindingList
	if err := in.List(ctx, &family, client.InNamespace(in.Namespace),
		client.MatchingFields{KeyHashIndex: hash}); err != nil {
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

// repositoryURL is the finding's repository, or empty when it names none. A
// cloud finding starts repo-less; whether it ever gets one is the enhancer
// chain's question, answered from the resource's ownership labels.
func repositoryURL(integ *v1alpha1.Integration, f source.Finding) string {
	if f.Repo.Owner == "" || f.Repo.Name == "" {
		return ""
	}
	return "https://" + githubHost(integ) + "/" + f.Repo.Owner + "/" + f.Repo.Name
}

// accumulationScope is what the finding is raised against — the thing two
// alerts must share, alongside their advisory, to be the same finding.
//
// For a cloud finding that is the resource, not its project: repository
// resolution is per-resource, so a family spanning resources could resolve to
// two different repositories and there would be no right answer. Scoping per
// resource means accumulation folds only SCC's re-notifications of the same
// (resource, category), which is exactly what it re-sends on every update.
func accumulationScope(repoURL string, f source.Finding) string {
	if repoURL != "" {
		return repoURL
	}
	if f.CloudResource != nil {
		return f.CloudResource.Name
	}
	return ""
}

// toCloudResource maps the seam's cloud resource onto the CR shape.
func toCloudResource(cr *source.CloudResource) *v1alpha1.FindingCloudResource {
	if cr == nil {
		return nil
	}
	return &v1alpha1.FindingCloudResource{
		Provider:    v1alpha1.CloudProvider(cr.Provider),
		Name:        cr.Name,
		Type:        cr.Type,
		Project:     cr.Project,
		Location:    cr.Location,
		DisplayName: cr.DisplayName,
	}
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
	labels := map[string]string{
		v1alpha1.LabelKeyHash:     hash,
		v1alpha1.LabelSource:      f.Source,
		v1alpha1.LabelIntegration: integ.Name,
		v1alpha1.LabelSeverity:    string(levelOf(f.Severity)),
	}
	// Omitted rather than hashed empty: a repo-less finding with a
	// real-looking repo-hash would read as belonging to some repository, and
	// the value would never be corrected once an enhancer resolved one.
	if repoURL != "" {
		labels[v1alpha1.LabelRepoHash] = hashOf(repoURL)
	}
	fnd := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: in.Namespace,
			Labels:    labels,
		},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: integ.Name},
			TrackingRef:    in.trackingRef(ctx, integ),
			Source:         f.Source,
			CloudResource:  toCloudResource(f.CloudResource),
			Advisories:     f.Advisories,
			RuleID:         f.RuleID,
			Title:          f.Title,
			Description:    truncate(f.Description, 65536),
			Severity:       levelOf(f.Severity),
			Alerts:         []v1alpha1.Alert{toAlert(f)},
		},
	}
	if repoURL != "" {
		fnd.Spec.Repository = &v1alpha1.FindingRepository{
			Type: v1alpha1.RepositoryTypeGitHub,
			URL:  repoURL,
			Name: f.Repo.String(),
		}
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
		slog.String("finding", name), slog.String("scope", accumulationScope(repoURL, f)))
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

// toAlert maps a scanner finding's alert fields. The id is the source's own
// string identifier where it has one; only sources that number their alerts
// fall back to the decimal form.
func toAlert(f source.Finding) v1alpha1.Alert {
	id := f.AlertID
	if id == "" {
		id = strconv.Itoa(f.AlertNumber)
	}
	a := v1alpha1.Alert{ID: id, Source: f.Source, URL: f.HTMLURL}
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
// issues-enabled one — a cloud or generic source has no issues capability of
// its own, but its findings still deserve tracking issues. Nil when none (or
// on a transient lookup failure): the finding is still tracked in-cluster.
func (in *Ingestor) trackingRef(ctx context.Context, integ *v1alpha1.Integration) *v1alpha1.LocalObjectReference {
	if issuesEnabled(integ) {
		return &v1alpha1.LocalObjectReference{Name: integ.Name}
	}
	tracker, err := selectIntegration(ctx, in.Client, in.Namespace, issuesEnabled)
	if err != nil {
		if !errors.Is(err, ErrNoIntegration) {
			in.log().LogAttrs(ctx, slog.LevelWarn, "tracking integration lookup failed",
				slog.String("integration", integ.Name), slog.Any("error", err))
		}
		return nil
	}
	return &v1alpha1.LocalObjectReference{Name: tracker.Name}
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
