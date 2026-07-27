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

// Transcript SSE event names. Turns arrive one per event and the stream always
// ends with `end`, so the client knows the difference between a finished
// conversation and a dropped connection.
const (
	eventTurn = "turn"
	eventEnd  = "end"
)

// Turn is one conversation entry on the wire. It mirrors the SPA's
// TranscriptTurn; keep the two in lockstep.
type Turn struct {
	Seq       int    `json:"seq"`
	At        string `json:"at,omitempty"`
	Role      string `json:"role"`
	Kind      string `json:"kind"`
	Tool      string `json:"tool,omitempty"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

func wireTurn(t transcript.Turn) Turn {
	return Turn{
		Seq: t.Seq, At: t.At, Role: string(t.Role), Kind: string(t.Kind),
		Tool: t.Tool, Text: t.Text, Truncated: t.Truncated,
	}
}

// handleTranscript streams one agent run's conversation.
//
// It is Server-Sent Events in both modes so the client has a single code path:
// a completed run replays its persisted transcript and ends, a running one
// replays what it has said so far and then follows live. This is deliberately
// unlike /events, which is public precisely because it carries no content —
// a transcript is finding data and is gated exactly like /api/findings.
func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r); !ok {
		return
	}
	finding := r.PathValue("name")
	kind := v1alpha1.RunKind(r.PathValue("kind"))
	attempt, err := strconv.Atoi(r.PathValue("attempt"))
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
	flusher.Flush()

	s.streamTranscript(w, r, flusher, run)
}

// runRef is the resolved run a transcript request names.
type runRef struct {
	name       string
	namespace  string
	transcript *v1alpha1.TranscriptRef
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
		ref.transcript = stage.Transcript
	}
	if job != nil {
		ref.jobName = job.Name
	}
	ref.running = phase == v1alpha1.RunRunning || phase == v1alpha1.RunPending
	return ref, nil
}

// streamTranscript writes the run's conversation and ends the stream. A
// persisted transcript wins over a live follow: once a run is collected its
// stored record is complete, while its pod log is already being reaped.
func (s *Server) streamTranscript(
	w http.ResponseWriter, r *http.Request, flusher http.Flusher, run runRef,
) {
	ctx := r.Context()
	if run.transcript != nil {
		turns, err := transcriptstore.Load(ctx, s.reader, run.namespace, run.transcript.Name)
		if err != nil {
			s.log.LogAttrs(ctx, slog.LevelError, "load transcript",
				slog.String("run", run.name), slog.Any("error", err))
		}
		for _, t := range turns {
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
	s.followTranscript(ctx, w, flusher, run)
}

// followTranscript replays what a running agent has said and then streams the
// rest until the run ends or the viewer leaves.
func (s *Server) followTranscript(
	ctx context.Context, w http.ResponseWriter, flusher http.Flusher, run runRef,
) {
	sub, err := s.tails.subscribe(run.jobName)
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
				writeEnd(w, flusher)
				return
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
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventTurn, payload); err != nil {
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
