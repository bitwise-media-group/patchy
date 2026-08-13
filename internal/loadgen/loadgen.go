// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package loadgen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// Fixed identities every generated object shares. The integration name enters
// the accumulation key, so a bench pairing SourceFinding with an Integration
// CR must name the CR IntegrationName.
const (
	// IntegrationName is the ingesting Integration every finding references.
	IntegrationName = "loadgen"
	// SourceID is the source handler id on every finding.
	SourceID = "loadgen-scanner"
	// Namespace the generated objects live in.
	Namespace = "patchy"
)

// baseTime anchors every generated timestamp; offsets are per-index so
// ordering is deterministic and spread.
var baseTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Opts shape a synthetic dataset. The zero value is usable.
type Opts struct {
	// AlertsPerFinding is the alert count folded into each finding
	// (default 1, capped at the CRD's 64).
	AlertsPerFinding int
	// PhaseMix weights the phase distribution; nil means every finding is
	// Opened. Terminal phases get a CompletedAt stamp.
	PhaseMix map[v1alpha1.Phase]int
	// RepoCardinality is the number of distinct repositories findings spread
	// over (default 100).
	RepoCardinality int
	// Seed varies the whole dataset; the default 0 is a valid seed.
	Seed uint64
}

func (o Opts) alerts() int {
	switch {
	case o.AlertsPerFinding <= 0:
		return 1
	case o.AlertsPerFinding > 64:
		return 64
	}
	return o.AlertsPerFinding
}

func (o Opts) repos() int {
	if o.RepoCardinality <= 0 {
		return 100
	}
	return o.RepoCardinality
}

// rng is the per-index deterministic random source: index i always sees the
// same stream regardless of how many other objects were generated.
func (o Opts) rng(i int) *rand.Rand {
	return rand.New(rand.NewPCG(o.Seed, uint64(i)))
}

// phase picks index i's phase from the mix, deterministically.
func (o Opts) phase(i int) v1alpha1.Phase {
	if len(o.PhaseMix) == 0 {
		return v1alpha1.PhaseOpened
	}
	// Iterate a sorted view so the pick is independent of map order.
	phases := make([]v1alpha1.Phase, 0, len(o.PhaseMix))
	total := 0
	for _, p := range []v1alpha1.Phase{
		v1alpha1.PhaseOpened, v1alpha1.PhaseEnhanced, v1alpha1.PhaseInvestigating,
		v1alpha1.PhaseQueued, v1alpha1.PhaseAwaitingApproval, v1alpha1.PhaseRemediating,
		v1alpha1.PhaseInReview, v1alpha1.PhaseRemediated, v1alpha1.PhaseFailed,
		v1alpha1.PhaseDismissed, v1alpha1.PhaseHandedOff,
	} {
		if w := o.PhaseMix[p]; w > 0 {
			phases = append(phases, p)
			total += w
		}
	}
	if total == 0 {
		return v1alpha1.PhaseOpened
	}
	pick := o.rng(i).IntN(total)
	for _, p := range phases {
		pick -= o.PhaseMix[p]
		if pick < 0 {
			return p
		}
	}
	return v1alpha1.PhaseOpened
}

// RepoURL is index i's repository URL (i modulo the repo cardinality).
func RepoURL(i int, o Opts) string {
	return fmt.Sprintf("https://github.com/loadgen/repo-%d", i%o.repos())
}

// repoName is the "owner/name" form of RepoURL.
func repoName(i int, o Opts) string {
	return fmt.Sprintf("loadgen/repo-%d", i%o.repos())
}

// advisory is index i's primary advisory — unique per index, so every index
// is its own accumulation family.
func advisory(i int) string {
	return fmt.Sprintf("CVE-2026-%06d", i)
}

// KeyHash is the accumulation-key label value for index i: the exact recipe
// internal/controller/integration persists (5-byte-hex sha256 over
// integration|source|scope|advisory).
func KeyHash(i int, o Opts) string {
	key := IntegrationName + "|" + SourceID + "|" + RepoURL(i, o) + "|" + advisory(i)
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// FindingName is index i's deterministic object name (generation 1).
func FindingName(i int, o Opts) string {
	return fmt.Sprintf("finding-%s-1", KeyHash(i, o))
}

// severities cycle deterministically across indices.
var severities = []v1alpha1.Level{
	v1alpha1.LevelLow, v1alpha1.LevelMedium, v1alpha1.LevelHigh, v1alpha1.LevelCritical,
}

// Finding builds index i's Finding, shaped like a real ingested one: the
// accumulation labels carry genuine hash values, alerts carry locations and
// snippets, and status matches the picked phase.
func Finding(i int, o Opts) *v1alpha1.Finding {
	hash := KeyHash(i, o)
	repoURL := RepoURL(i, o)
	severity := severities[i%len(severities)]
	created := metav1.NewTime(baseTime.Add(time.Duration(i) * time.Second))

	fnd := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              FindingName(i, o),
			Namespace:         Namespace,
			CreationTimestamp: created,
			Labels: map[string]string{
				v1alpha1.LabelKeyHash:     hash,
				v1alpha1.LabelSource:      SourceID,
				v1alpha1.LabelIntegration: IntegrationName,
				v1alpha1.LabelSeverity:    string(severity),
				v1alpha1.LabelRepoHash:    hashOf(repoURL),
			},
		},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: IntegrationName},
			Source:         SourceID,
			Advisories:     []string{advisory(i), fmt.Sprintf("CWE-%d", 1+i%400)},
			RuleID:         fmt.Sprintf("rule/%d", i%50),
			Title:          fmt.Sprintf("Synthetic finding %d", i),
			Description:    description(i, o),
			Severity:       severity,
			Repository: &v1alpha1.FindingRepository{
				Type:          v1alpha1.RepositoryTypeGitHub,
				URL:           repoURL,
				Name:          repoName(i, o),
				DefaultBranch: "main",
			},
		},
	}
	for j := range o.alerts() {
		fnd.Spec.Alerts = append(fnd.Spec.Alerts, v1alpha1.Alert{
			ID:     alertID(i, j),
			Source: SourceID,
			URL:    fmt.Sprintf("%s/security/alerts/%d", repoURL, j),
			Locations: []v1alpha1.Location{{
				Path:      fmt.Sprintf("src/pkg%d/file%d.go", j%8, j),
				StartLine: int32(10 + j),
				EndLine:   int32(12 + j),
				Snippet:   fmt.Sprintf("value := input[%d] // tainted", j),
			}},
		})
	}

	phase := o.phase(i)
	first := metav1.NewTime(created.Time)
	until := metav1.NewTime(created.Add(time.Hour))
	fnd.Status = v1alpha1.FindingStatus{
		Phase:           phase,
		PhaseTimes:      []v1alpha1.PhaseTime{{Phase: phase, At: created}},
		FirstObservedAt: &first,
		AccumulateUntil: &until,
	}
	if v1alpha1.Terminal(phase) {
		done := metav1.NewTime(created.Add(2 * time.Hour))
		fnd.Status.CompletedAt = &done
	}
	if phase != v1alpha1.PhaseOpened {
		fnd.Status.Owners = []string{fmt.Sprintf("owner-%d", i%20)}
		fnd.Status.Enrichments = []v1alpha1.Enrichment{{
			Enhancer:   "loadgen-cmdb",
			Owners:     fnd.Status.Owners,
			Attributes: map[string]string{"system": fmt.Sprintf("system-%d", i%10)},
			Markdown:   fmt.Sprintf("synthetic enrichment for finding %d", i),
			AppliedAt:  created,
		}}
	}
	return fnd
}

// Findings builds n findings for indices [0, n).
func Findings(n int, o Opts) []v1alpha1.Finding {
	out := make([]v1alpha1.Finding, 0, n)
	for i := range n {
		out = append(out, *Finding(i, o))
	}
	return out
}

// SourceFinding builds the scanner-side alert whose accumulation key matches
// Finding(i, o) — ingesting it against an Integration named IntegrationName
// folds into that family. alert distinguishes deliveries within the family.
func SourceFinding(i, alert int, o Opts) source.Finding {
	return source.Finding{
		Source:      SourceID,
		Repo:        source.Repo{Owner: "loadgen", Name: fmt.Sprintf("repo-%d", i%o.repos())},
		AlertID:     alertID(i, alert),
		Advisories:  []string{advisory(i), fmt.Sprintf("CWE-%d", 1+i%400)},
		RuleID:      fmt.Sprintf("rule/%d", i%50),
		Title:       fmt.Sprintf("Synthetic finding %d", i),
		Description: description(i, o),
		Severity:    string(severities[i%len(severities)]),
		HTMLURL:     fmt.Sprintf("%s/security/alerts/%d", RepoURL(i, o), alert),
		Locations: []source.Location{{
			Path:      fmt.Sprintf("src/pkg%d/file%d.go", alert%8, alert),
			StartLine: 10 + alert,
			EndLine:   12 + alert,
			Snippet:   fmt.Sprintf("value := input[%d] // tainted", alert),
		}},
	}
}

// Rollups builds n FindingRollup objects: index 0 is the total scope, the
// rest are repository scopes, each with a populated bucket, monthly trend,
// and a full 512-entry ledger — the shape a long-lived deployment carries.
func Rollups(n int) []v1alpha1.FindingRollup {
	out := make([]v1alpha1.FindingRollup, 0, n)
	for i := range n {
		scope := v1alpha1.RollupScope{Type: v1alpha1.ScopeTotal}
		if i > 0 {
			scope = v1alpha1.RollupScope{
				Type: v1alpha1.ScopeRepository,
				Key:  fmt.Sprintf("loadgen/repo-%d", i-1),
			}
		}
		first := metav1.NewTime(baseTime)
		last := metav1.NewTime(baseTime.Add(time.Duration(i+1) * time.Hour))
		fr := v1alpha1.FindingRollup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rollupName(scope, i),
				Namespace: Namespace,
				Labels:    map[string]string{v1alpha1.LabelScope: string(scope.Type)},
			},
			Spec: v1alpha1.FindingRollupSpec{Scope: scope},
			Status: v1alpha1.FindingRollupStatus{
				SchemaVersion:  v1alpha1.RollupSchemaVersion,
				FirstProcessed: &first,
				LastProcessed:  &last,
				Bucket: v1alpha1.RollupBucket{
					Findings: int64(100 + i),
					Phases: map[string]int64{
						"remediated": int64(60 + i), "dismissed": 20, "handedoff": 15, "failed": 5,
					},
					Recommendations: map[string]int64{"remediate": int64(70 + i), "ignore": 20, "manual": 10},
					Attempts:        int64(150 + i),
					Stages: map[string]v1alpha1.StageAggregate{
						"investigation": stageAggregate(i, 1),
						"remediation":   stageAggregate(i, 2),
					},
				},
				Monthly: monthly(i),
				Recent:  ledger(i),
			},
		}
		out = append(out, fr)
	}
	return out
}

// Investigations builds n Investigation children; child i belongs to
// Finding(i, o) and carries a completed stage result.
func Investigations(n int, o Opts) []v1alpha1.Investigation {
	out := make([]v1alpha1.Investigation, 0, n)
	for i := range n {
		finding := FindingName(i, o)
		started := metav1.NewTime(baseTime.Add(time.Duration(i) * time.Second))
		finished := metav1.NewTime(started.Add(5 * time.Minute))
		inv := v1alpha1.Investigation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      finding + "-inv-1",
				Namespace: Namespace,
				Labels: map[string]string{
					v1alpha1.LabelFinding: finding,
					v1alpha1.LabelAttempt: "1",
				},
			},
			Spec: v1alpha1.InvestigationSpec{
				FindingRef: v1alpha1.ObjectReference{Name: finding},
				Attempt:    1,
			},
			Status: v1alpha1.InvestigationStatus{
				Phase:          v1alpha1.RunComplete,
				Recommendation: v1alpha1.RecommendationRemediate,
				Confidence:     "0.9",
				Report:         fmt.Sprintf("## Analysis %d\n\nsynthetic report body\n", i),
				Stage: &v1alpha1.StageResult{
					Outcome:  "ok",
					Harness:  "claude",
					Model:    "anthropic/claude-sonnet-5",
					NumTurns: int32(10 + i%20),
					Usage: v1alpha1.UsageSummary{
						InputTokens:     int64(50000 + i),
						OutputTokens:    int64(8000 + i),
						CacheReadTokens: int64(200000 + i),
						CostUSD:         "1.25",
					},
					StartedAt:           &started,
					FinishedAt:          &finished,
					ElapsedMilliseconds: 300000,
				},
			},
		}
		out = append(out, inv)
	}
	return out
}

// alertID is deterministic per (finding index, alert ordinal).
func alertID(i, j int) string {
	return fmt.Sprintf("lg-%d-%d", i, j)
}

// description pads deterministically-random prose so finding size resembles
// a scanner's (~1KB).
func description(i int, o Opts) string {
	r := o.rng(i)
	b := make([]byte, 1024)
	for k := range b {
		b[k] = byte('a' + r.IntN(26))
	}
	return fmt.Sprintf("Synthetic description for finding %d: %s", i, b)
}

// rollupName mirrors stats.ScopeObjectName without importing it (loadgen
// stays a leaf package): total, else repo-<hash>.
func rollupName(scope v1alpha1.RollupScope, i int) string {
	if scope.Type == v1alpha1.ScopeTotal {
		return "total"
	}
	sum := sha256.Sum256([]byte(scope.Key))
	return "repo-" + hex.EncodeToString(sum[:5]) + fmt.Sprintf("-%d", i)
}

// stageAggregate is a plausibly-sized stage bucket.
func stageAggregate(i, mult int) v1alpha1.StageAggregate {
	return v1alpha1.StageAggregate{
		Runs:                int64(mult * (100 + i)),
		Succeeded:           int64(mult * (80 + i)),
		Outcomes:            map[string]int64{"ok": int64(mult * (80 + i)), "timeout": 10, "runtime_error": 10},
		InputTokens:         int64(mult) * 5_000_000,
		OutputTokens:        int64(mult) * 800_000,
		CacheReadTokens:     int64(mult) * 20_000_000,
		CacheCreationTokens: int64(mult) * 1_000_000,
		CostMicroUSD:        int64(mult) * 125_000_000,
		ElapsedMilliseconds: int64(mult) * 30_000_000,
		Turns:               int64(mult) * 1_500,
		Estimate: &v1alpha1.EstimateAggregate{
			Runs:                  int64(mult * 50),
			PredictedTurns:        int64(mult * 1000),
			ActualTurns:           int64(mult * 1200),
			PredictedOutputTokens: int64(mult) * 400_000,
			ActualOutputTokens:    int64(mult) * 500_000,
		},
	}
}

// monthly is a 24-month trend line.
func monthly(i int) map[string]v1alpha1.MonthlyBucket {
	out := make(map[string]v1alpha1.MonthlyBucket, 24)
	for m := range 24 {
		key := baseTime.AddDate(0, -m, 0).Format("2006-01")
		out[key] = v1alpha1.MonthlyBucket{
			Findings:     int64(10 + m + i),
			Runs:         int64(20 + m),
			CostMicroUSD: int64(m) * 1_000_000,
		}
	}
	return out
}

// ledger is a full 512-entry exactly-once ledger.
func ledger(i int) []string {
	out := make([]string, 0, 512)
	for k := range 512 {
		out = append(out, fmt.Sprintf("i:ldg-%d-%d", i, k))
	}
	return out
}

// hashOf mirrors the ingester's repo-hash label recipe.
func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:5])
}
