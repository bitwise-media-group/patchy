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

// Stream bounds: what the in-pod recorder admits onto stdout, and so what a
// live viewer sees. They are generous because the live stream is the whole
// conversation; only the total stays bounded, because the kubelet rotates a
// container log at 10 MiB by default and a non-follow read of the stage result
// sees only the current file, so an unbounded conversation could rotate the
// result events out of reach. MaxTurns of zero is unbounded.
const (
	DefaultStreamMaxTurnBytes  = 64 << 10
	DefaultStreamMaxTurns      = 0
	DefaultStreamMaxTotalBytes = 8 << 20
)

// Store bounds: what Compact keeps for the persisted record. A transcript is
// stored in a ConfigMap, so the total is sized to leave headroom under the
// 1 MiB object cap even before gzip; the per-turn cap keeps one runaway tool
// result from consuming the whole budget.
const (
	DefaultStoreMaxTurnBytes  = 2048
	DefaultStoreMaxTurns      = 500
	DefaultStoreMaxTotalBytes = 512 << 10
)

// Redacted replaces any credential value found in captured text.
const Redacted = "«redacted»"

// Limits bounds one recording. A zero field takes the default for where the
// limits apply (stream or store); a negative field disables that bound.
type Limits struct {
	MaxTurnBytes  int
	MaxTurns      int
	MaxTotalBytes int
}

// StreamDefaults are the recorder's bounds when a field is left zero.
var StreamDefaults = Limits{
	MaxTurnBytes:  DefaultStreamMaxTurnBytes,
	MaxTurns:      DefaultStreamMaxTurns,
	MaxTotalBytes: DefaultStreamMaxTotalBytes,
}

// StoreDefaults are Compact's bounds when a field is left zero.
var StoreDefaults = Limits{
	MaxTurnBytes:  DefaultStoreMaxTurnBytes,
	MaxTurns:      DefaultStoreMaxTurns,
	MaxTotalBytes: DefaultStoreMaxTotalBytes,
}

// resolve fills each zero field from defaults.
func (l Limits) resolve(defaults Limits) Limits {
	if l.MaxTurnBytes == 0 {
		l.MaxTurnBytes = defaults.MaxTurnBytes
	}
	if l.MaxTurns == 0 {
		l.MaxTurns = defaults.MaxTurns
	}
	if l.MaxTotalBytes == 0 {
		l.MaxTotalBytes = defaults.MaxTotalBytes
	}
	return l
}

// Recorder normalises, bounds, redacts, and sequences the turns of one run.
// It is the single gate every captured turn passes through on its way to
// stdout, so it enforces the stream bounds; the persisted record is a Compact
// of what it emitted, never a second capture that could disagree with it.
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

// MinSecretLen is the shortest value Redact scrubs. A shorter one would
// redact half of any text by accident.
const MinSecretLen = 8

// Scrubbable keeps the values long enough to scrub, in order.
func Scrubbable(secrets []string) []string {
	keep := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if len(s) >= MinSecretLen {
			keep = append(keep, s)
		}
	}
	return keep
}

// Redact replaces every occurrence of each secret in text with Redacted,
// in order, so a longer value listed before its own prefix is scrubbed
// whole. Values shorter than MinSecretLen are left alone.
func Redact(text string, secrets []string) string {
	for _, s := range secrets {
		if len(s) >= MinSecretLen {
			text = strings.ReplaceAll(text, s, Redacted)
		}
	}
	return text
}

// NewRecorder returns a recorder that hands each admitted turn to emit.
// secrets are literal credential values scrubbed from every captured field:
// the pod can hold secrets (the broker caller token, any credential-named
// environment variable) and a tool result can echo them.
func NewRecorder(limits Limits, secrets []string, emit func(Turn)) *Recorder {
	return &Recorder{
		limits:  limits.resolve(StreamDefaults),
		secrets: Scrubbable(secrets),
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

// Notice emits a recorder-voiced turn, such as a budget abort or a stage
// banner. It is bounded like any other turn but never itself causes
// truncation.
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

	text := Redact(string(ansi.Strip([]byte(t.Text))), r.secrets)
	if r.limits.MaxTurnBytes > 0 {
		cut, did := Truncate(text, r.limits.MaxTurnBytes)
		text, t.Truncated = cut, t.Truncated || did
	}
	t.Text = text
	t.Tool = string(ansi.Strip([]byte(t.Tool)))
	t.Path = Redact(string(ansi.Strip([]byte(t.Path))), r.secrets)
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
// bounds that triggered it, because it is the one turn a reader must always
// see.
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
