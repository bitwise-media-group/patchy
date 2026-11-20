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
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/transcript"
	"github.com/bitwise-media-group/patchy/internal/transcriptstore"
)

// Transcript SSE event names. Turns arrive one per event, each with its Seq as
// the event id, and a complete stream ends with `end`, so the client knows the
// difference between a finished conversation and a dropped connection. A
// stream that closes without `end` (a dropped connection, or a viewer evicted
// for falling behind) is resumed by reconnecting with Last-Event-ID.
const (
	eventTurn = "turn"
	eventEnd  = "end"
)

// transcriptRetry is the reconnect delay, in milliseconds, the stream asks the
// client's EventSource to use.
const transcriptRetry = 1000

// Turn is one conversation entry on the wire. It mirrors the SPA's
// TranscriptTurn; keep the two in lockstep.
type Turn struct {
	Seq  int    `json:"seq"`
	At   string `json:"at,omitempty"`
	Role string `json:"role"`
	Kind string `json:"kind"`
	Tool string `json:"tool,omitempty"`
	// ID pairs a tool_use turn with its tool_result.
	ID string `json:"id,omitempty"`
	// Path is the file a tool call targets.
	Path string `json:"path,omitempty"`
	// Syntax is a render hint for Text: "diff", "shell", or empty (infer
	// from Path).
	Syntax    string `json:"syntax,omitempty"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

func wireTurn(t transcript.Turn) Turn {
	return Turn{
		Seq: t.Seq, At: t.At, Role: string(t.Role), Kind: string(t.Kind),
		Tool: t.Tool, ID: t.ID, Path: t.Path, Syntax: string(t.Syntax),
		Text: t.Text, Truncated: t.Truncated,
	}
}

// resumeAfter is the Seq a reconnecting client already has: the standard
// Last-Event-ID header an EventSource sends on its own reconnects, or the
// ?after= query a client uses when it opens a fresh stream to resume. Zero
// (absent or malformed) replays from the first turn.
func resumeAfter(r *http.Request) int {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 0 {
		return 0
	}
	return int(n)
}

// handleTranscript streams one agent run's conversation.
//
// It is Server-Sent Events in every mode so the client has a single code path:
// a completed run replays its persisted transcript and ends, a running one
// replays what it has said so far and then follows live, and an archived one
// sends `archived` and ends, so the client plays it back from the archive. Unlike /events,
// which carries no content and admits any read grant, a transcript is finding
// data and is gated exactly like /api/findings.
func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r); !ok {
		return
	}
	finding := r.PathValue("name")
	kind := v1alpha1.RunKind(r.PathValue("kind"))
	// Parsed at the width it is used at: Atoi returns a 64-bit int on the
	// platforms this ships on, so an out-of-range attempt would wrap into a
	// valid-looking one (4294967297 becomes attempt 1) instead of being
	// rejected here.
	attempt, err := strconv.ParseInt(r.PathValue("attempt"), 10, 32)
	if err != nil || attempt < 1 {
		http.Error(w, "invalid attempt", http.StatusBadRequest)
		return
	}

	run, err := s.findRun(r.Context(), finding, kind, int32(attempt))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	_, _ = fmt.Fprintf(w, "retry: %d\n\n", transcriptRetry)
	flusher.Flush()

	s.streamTranscript(w, r, flusher, run, resumeAfter(r))
}

// runRef is the resolved run a transcript request names.
type runRef struct {
	name       string
	namespace  string
	transcript *v1alpha1.TranscriptRef
	archive    *v1alpha1.ArchiveRef
	jobName    string
	running    bool
}

// findRun resolves and authorises the run a request names. The finding is
// re-checked against the child's own findingRef rather than trusted from the
// path: the run name is derivable, so a caller must not be able to read one
// finding's transcript by naming another finding's attempt.
func (s *Server) findRun(
	ctx context.Context, finding string, kind v1alpha1.RunKind, attempt int32,
) (runRef, error) {
	var (
		ref   runRef
		owner string
		phase v1alpha1.RunPhase
		stage *v1alpha1.StageResult
		job   *v1alpha1.JobReference
	)
	switch kind {
	case v1alpha1.RunKindInvestigation:
		var inv v1alpha1.Investigation
		ref.name = fmt.Sprintf("%s-inv-%d", finding, attempt)
		if err := s.client.Get(ctx, types.NamespacedName{Namespace: s.namespace, Name: ref.name}, &inv); err != nil {
			return ref, errors.New("no such investigation")
		}
		owner = inv.Spec.FindingRef.Name
		phase, stage, job = inv.Status.Phase, inv.Status.Stage, inv.Status.JobRef
	case v1alpha1.RunKindRemediation:
		var rem v1alpha1.Remediation
		ref.name = fmt.Sprintf("%s-rem-%d", finding, attempt)
		if err := s.client.Get(ctx, types.NamespacedName{Namespace: s.namespace, Name: ref.name}, &rem); err != nil {
			return ref, errors.New("no such remediation")
		}
		owner = rem.Spec.FindingRef.Name
		phase, stage, job = rem.Status.Phase, rem.Status.Stage, rem.Status.JobRef
	default:
		return ref, errors.New("unknown run kind")
	}
	if owner != finding {
		return ref, fmt.Errorf("no such run for finding %s", finding)
	}

	ref.namespace = s.namespace
	if stage != nil {
		ref.transcript, ref.archive = stage.Transcript, stage.Archive
	}
	if job != nil {
		ref.jobName = job.Name
	}
	ref.running = phase == v1alpha1.RunRunning || phase == v1alpha1.RunPending
	return ref, nil
}

// streamTranscript writes the run's conversation after Seq after and ends the
// stream. A persisted transcript wins over a live follow: once a run is
// collected its stored record is final, while its pod log is already being
// reaped.
func (s *Server) streamTranscript(
	w http.ResponseWriter, r *http.Request, flusher http.Flusher, run runRef, after int,
) {
	ctx := r.Context()
	if run.transcript != nil && run.transcript.Object != "" {
		// Archived: the client fetches the full record straight from the
		// bucket through the archive route.
		writeArchived(w, flusher)
		return
	}
	if run.transcript != nil {
		turns, err := transcriptstore.Load(ctx, s.reader, run.namespace, run.transcript.Name)
		if err != nil {
			s.log.LogAttrs(ctx, slog.LevelError, "load transcript",
				slog.String("run", run.name), slog.Any("error", err))
		}
		for _, t := range turns {
			if t.Seq <= after {
				continue
			}
			if !writeTurn(w, flusher, t) {
				return
			}
		}
		writeEnd(w, flusher)
		return
	}

	if !run.running || run.jobName == "" {
		// Nothing recorded and nothing running: an empty conversation, which
		// the client renders as "no transcript" rather than as an error.
		writeEnd(w, flusher)
		return
	}
	s.followTranscript(ctx, w, flusher, run, after)
}

// followTranscript replays what a running agent has said and then streams the
// rest until the run ends or the viewer leaves. A viewer evicted for falling
// behind gets no `end`: the stream closes, and the client reconnects
// from the last turn it saw.
func (s *Server) followTranscript(
	ctx context.Context, w http.ResponseWriter, flusher http.Flusher, run runRef, after int,
) {
	sub, err := s.tails.subscribe(run.jobName, after)
	if err != nil {
		// Out of follow budget. The run is still live, so ending the stream
		// cleanly beats an error: the client retries and meanwhile shows
		// whatever the dataset already reports.
		writeEnd(w, flusher)
		return
	}
	defer sub.Close()

	for _, t := range sub.Replay {
		if !writeTurn(w, flusher, t) {
			return
		}
	}

	ping := time.NewTicker(keepalivePeriod)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-sub.Turns:
			if !ok {
				if !sub.Lagged() {
					writeEnd(w, flusher)
				}
				return
			}
			if t.Seq <= after {
				continue // a fresh follow replays the log from its start
			}
			if !writeTurn(w, flusher, t) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeTurn emits one turn event, reporting whether the client is still there.
func writeTurn(w http.ResponseWriter, flusher http.Flusher, t transcript.Turn) bool {
	payload, err := json.Marshal(wireTurn(t))
	if err != nil {
		return true // skip the turn, keep the stream
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", t.Seq, eventTurn, payload); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

// writeEnd closes the conversation, distinguishing a finished stream from a
// dropped connection.
func writeEnd(w http.ResponseWriter, flusher http.Flusher) {
	_, _ = fmt.Fprintf(w, "event: %s\ndata: {}\n\n", eventEnd)
	flusher.Flush()
}
