// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package transcript

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bitwise-media-group/patchy/internal/ansi"
)

// Recording bounds. A transcript is persisted in a ConfigMap, so the total is
// sized to leave headroom under the 1 MiB object cap even before gzip; the
// per-turn cap keeps one runaway tool result from consuming the whole budget.
const (
	DefaultMaxTurnBytes  = 2048
	DefaultMaxTurns      = 500
	DefaultMaxTotalBytes = 512 << 10
)

// Redacted replaces any credential value found in captured text.
const Redacted = "«redacted»"

// Limits bounds one recording. A zero field takes the package default; a
// negative field disables that bound.
type Limits struct {
	MaxTurnBytes  int
	MaxTurns      int
	MaxTotalBytes int
}

func (l Limits) resolve() Limits {
	if l.MaxTurnBytes == 0 {
		l.MaxTurnBytes = DefaultMaxTurnBytes
	}
	if l.MaxTurns == 0 {
		l.MaxTurns = DefaultMaxTurns
	}
	if l.MaxTotalBytes == 0 {
		l.MaxTotalBytes = DefaultMaxTotalBytes
	}
	return l
}

// Recorder normalises, bounds, redacts, and sequences the turns of one run.
// It is the single gate every captured turn passes through, so the live stream
// and the persisted transcript can never disagree.
//
// Recorder is safe for concurrent use, though the runner's line observer calls
// it from one goroutine.
type Recorder struct {
	mu      sync.Mutex
	limits  Limits
	secrets []string
	now     func() time.Time
	emit    func(Turn)

	seq       int
	total     int
	stopped   bool
	truncated bool
}

// NewRecorder returns a recorder that hands each admitted turn to emit.
// secrets are literal credential values scrubbed from every captured field:
// the pod holds the model API key and a tool result can echo the environment.
func NewRecorder(limits Limits, secrets []string, emit func(Turn)) *Recorder {
	keep := make([]string, 0, len(secrets))
	for _, s := range secrets {
		// A short or empty value would redact half the transcript.
		if len(s) >= 8 {
			keep = append(keep, s)
		}
	}
	return &Recorder{
		limits:  limits.resolve(),
		secrets: keep,
		now:     time.Now,
		emit:    emit,
	}
}

// Record admits one turn: it stamps the sequence and time, redacts, caps the
// text, and emits. Turns arriving after a bound is hit are dropped, and the
// first drop emits a closing notice so the reader knows the record is partial.
func (r *Recorder) Record(t Turn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record(t)
}

// RecordAll admits a batch, preserving order.
func (r *Recorder) RecordAll(turns []Turn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range turns {
		r.record(t)
	}
}

// Notice emits a recorder-voiced turn — a budget abort, a stage banner. It is
// bounded like any other turn but is never itself the cause of truncation.
func (r *Recorder) Notice(format string, args ...any) {
	r.Record(Turn{Role: RoleSystem, Kind: KindNotice, Text: fmt.Sprintf(format, args...)})
}

// Stats reports what the recorder admitted, for the stage result.
func (r *Recorder) Stats() (turns int, truncated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq, r.truncated
}

// record is the unlocked body of Record.
func (r *Recorder) record(t Turn) {
	if r.stopped || t.Kind == "" {
		return
	}
	if r.limits.MaxTurns > 0 && r.seq >= r.limits.MaxTurns {
		r.stop("transcript truncated: %d turn cap reached", r.limits.MaxTurns)
		return
	}
	if r.limits.MaxTotalBytes > 0 && r.total >= r.limits.MaxTotalBytes {
		r.stop("transcript truncated: %d byte cap reached", r.limits.MaxTotalBytes)
		return
	}

	text := string(ansi.Strip([]byte(t.Text)))
	for _, s := range r.secrets {
		text = strings.ReplaceAll(text, s, Redacted)
	}
	if r.limits.MaxTurnBytes > 0 {
		cut, did := Truncate(text, r.limits.MaxTurnBytes)
		text, t.Truncated = cut, t.Truncated || did
	}
	t.Text = text
	t.Tool = string(ansi.Strip([]byte(t.Tool)))
	if t.Truncated {
		r.truncated = true
	}

	r.seq++
	r.total += len(text)
	t.Seq = r.seq
	t.At = r.now().UTC().Format(time.RFC3339)
	r.emit(t)
}

// stop emits the closing notice and halts recording. The notice bypasses the
// bounds that triggered it — it is the one turn a reader must always see.
func (r *Recorder) stop(format string, args ...any) {
	r.stopped, r.truncated = true, true
	r.seq++
	r.emit(Turn{
		Seq:       r.seq,
		At:        r.now().UTC().Format(time.RFC3339),
		Role:      RoleSystem,
		Kind:      KindNotice,
		Text:      fmt.Sprintf(format, args...),
		Truncated: true,
	})
}
