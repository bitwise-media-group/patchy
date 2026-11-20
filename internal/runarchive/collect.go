// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package runarchive

import (
	"context"
	"log/slog"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/agentresult"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/transcriptstore"
)

// uploadTimeout bounds one run's archive upload. The run controllers
// reconcile serially, so a bucket call that hangs would otherwise stall every
// other run behind it; a timeout is just another failed attempt, retried
// within the window.
const uploadTimeout = 2 * time.Minute

// OutcomeArchiveFailed is the run outcome when a required archive could not
// be written: the run fails and nothing downstream (verdict, push) happens.
const OutcomeArchiveFailed = "archive_failed"

// scopeName identifies this package's meter.
const scopeName = "github.com/bitwise-media-group/patchy/internal/runarchive"

// failures counts archives abandoned after the retry window. Exported to
// Prometheus as patchy_archive_failures_total{kind,provider}.
var failures = sync.OnceValue(func() metric.Int64Counter {
	c, _ := otel.Meter(scopeName).Int64Counter("patchy.archive.failures",
		metric.WithDescription("agent runs whose archive could not be written within the retry window"))
	return c
})

// pushFailures counts push.json records that could not be written.
var pushFailures = sync.OnceValue(func() metric.Int64Counter {
	c, _ := otel.Meter(scopeName).Int64Counter("patchy.archive.push_record_failures",
		metric.WithDescription("remediation push records that could not be archived"))
	return c
})

// Record is what collection decided a run's status should carry.
type Record struct {
	// Transcript locates the conversation: an archive object or a ConfigMap.
	Transcript *v1alpha1.TranscriptRef
	// Archive locates the run's archive, when the upload succeeded.
	Archive *v1alpha1.ArchiveRef
	// Condition is the Archived condition; nil when archival is off.
	Condition *metav1.Condition
	// FailDetail is non-empty when a required archive failed: the run must
	// fail with OutcomeArchiveFailed and this detail.
	FailDetail string
}

// Apply stamps the record onto a run's stage and conditions. A nil record
// clears the references.
func (r *Record) Apply(stage *v1alpha1.StageResult, conds *[]metav1.Condition, generation int64) {
	if r == nil {
		stage.Transcript, stage.Archive = nil, nil
		return
	}
	stage.Transcript, stage.Archive = r.Transcript, r.Archive
	if r.Condition != nil {
		c := *r.Condition
		c.ObservedGeneration = generation
		meta.SetStatusCondition(conds, c)
	}
}

// Collector persists what a finished run said: to the archive when one is
// configured, otherwise (or on fail-open fallback) as a ConfigMap transcript.
type Collector struct {
	// Client writes ConfigMap transcripts.
	Client client.Client
	// Archiver is nil when archival is disabled.
	Archiver *Archiver
	// Events receives the Warning for an abandoned archive; nil discards.
	Events events.EventRecorder
	// Log receives diagnostics; nil discards.
	Log *slog.Logger
}

// Target is where a ConfigMap transcript goes, for the non-archive path.
type Target struct {
	Labels map[string]string
	GVK    metav1.TypeMeta
}

// Collect decides the run's record. A positive retry duration means the
// archive upload failed but the window is still open: the caller should
// requeue after it and change nothing, since the next pass re-reads the same
// pod log and rewrites identical objects. Collect itself never fails the
// reconcile: a transcript that cannot be stored is logged and dropped,
// because losing the explanation must never cost the result it explains.
func (c *Collector) Collect(ctx context.Context, run Run, out jobs.RunOutput, t Target) (*Record, time.Duration) {
	if c.Archiver == nil {
		return &Record{Transcript: c.persist(ctx, run, out, t)}, 0
	}
	a := c.Archiver
	uploadCtx, cancel := context.WithTimeout(ctx, uploadTimeout)
	ref, err := a.Archive(uploadCtx, run, out)
	cancel()
	if err == nil {
		rec := &Record{
			Archive: ref,
			Condition: &metav1.Condition{
				Type: v1alpha1.ConditionArchived, Status: metav1.ConditionTrue,
				Reason: v1alpha1.ReasonArchived, Message: "archived to " + ref.Prefix,
			},
		}
		if _, ok := ref.Object(v1alpha1.ArchiveObjectTranscript); ok {
			rec.Transcript = &v1alpha1.TranscriptRef{
				Object:    v1alpha1.ArchiveObjectTranscript,
				Turns:     int32(len(out.Turns)),
				Truncated: transcriptTruncated(out),
			}
		}
		return rec, 0
	}

	if retry, after := a.retryable(err, run.FinishedAt); retry {
		c.log().LogAttrs(ctx, slog.LevelWarn, "archive upload failed; will retry",
			slog.String("run", run.Object.GetName()), slog.Duration("retry_after", after), slog.Any("error", err))
		return nil, after
	}

	failures().Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", string(run.Kind)),
		attribute.String("provider", string(a.Config.Provider)),
	))
	detail := agentresult.TruncateDetail("archive upload failed: " + err.Error())
	c.log().LogAttrs(ctx, slog.LevelError, "archive abandoned",
		slog.String("run", run.Object.GetName()), slog.Bool("required", a.Required), slog.Any("error", err))
	if c.Events != nil {
		action := "falling back to the ConfigMap transcript"
		if a.Required {
			action = "failing the run (archive required)"
		}
		c.Events.Eventf(run.Object, nil, corev1.EventTypeWarning, v1alpha1.ReasonArchiveFailed, "Archive",
			"%s; %s", detail, action)
	}
	rec := &Record{
		// Even a fail-closed run keeps its transcript: the archive failing
		// is no reason to also lose what the agent said.
		Transcript: c.persist(ctx, run, out, t),
		Condition: &metav1.Condition{
			Type: v1alpha1.ConditionArchived, Status: metav1.ConditionFalse,
			Reason: v1alpha1.ReasonArchiveFailed, Message: detail,
		},
	}
	if a.Required {
		rec.FailDetail = detail
	}
	return rec, 0
}

// RecordPush writes push.json for an archived remediation, best-effort: the
// pull request is already open, so a failure is logged and counted only.
func (c *Collector) RecordPush(ctx context.Context, ref *v1alpha1.ArchiveRef, p Push) {
	if c.Archiver == nil || ref == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	if err := c.Archiver.RecordPush(ctx, ref, p); err != nil {
		pushFailures().Add(ctx, 1, metric.WithAttributes(
			attribute.String("provider", string(c.Archiver.Config.Provider))))
		c.log().LogAttrs(ctx, slog.LevelWarn, "archive push record failed",
			slog.String("prefix", ref.Prefix), slog.Any("error", err))
	}
}

func (c *Collector) persist(ctx context.Context, run Run, out jobs.RunOutput, t Target) *v1alpha1.TranscriptRef {
	ref, err := transcriptstore.Persist(ctx, c.Client, run.Object.GetNamespace(), t.Labels, run.Object, t.GVK, out.Turns)
	if err != nil {
		c.log().LogAttrs(ctx, slog.LevelError, "persist transcript",
			slog.String("run", run.Object.GetName()), slog.Any("error", err))
	}
	return ref
}

func (c *Collector) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log
}

// transcriptTruncated reports whether the in-pod recorder cut the stream.
func transcriptTruncated(out jobs.RunOutput) bool {
	for _, t := range out.Turns {
		if t.Truncated {
			return true
		}
	}
	return false
}
