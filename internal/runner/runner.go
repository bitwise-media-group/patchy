// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// scopeName is this package's OpenTelemetry instrumentation scope.
const scopeName = "github.com/bitwise-media-group/patchy/internal/runner"

// CommandSpec describes a runner invocation. Harnesses build specs but never
// touch os/exec; this package executes them, so callers can fake execution
// entirely.
type CommandSpec struct {
	Argv []string // Argv[0] is replaced with the resolved CLI path before exec
	Dir  string   // workspace the agent runs in
	Env  []string // extras appended to os.Environ()
}

// obs lazily builds the tracer and instruments on first Run, after telemetry
// has installed the global providers; before then otel's globals are no-ops,
// so instrumentation is harmless when telemetry is disabled.
var obs = sync.OnceValue(newObservability)

// observability holds the agent-exec span tracer and its metrics.
type observability struct {
	tracer       trace.Tracer
	execDuration metric.Float64Histogram
	timeouts     metric.Int64Counter
}

func newObservability() *observability {
	m := otel.Meter(scopeName)
	dur, err := m.Float64Histogram("patchy.agent.exec.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Wall-clock duration of one agent CLI invocation."))
	if err != nil {
		otel.Handle(err)
	}
	to, err := m.Int64Counter("patchy.agent.exec.timeout",
		metric.WithUnit("{exec}"),
		metric.WithDescription("Agent CLI invocations that hit the per-run timeout."))
	if err != nil {
		otel.Handle(err)
	}
	return &observability{tracer: otel.Tracer(scopeName), execDuration: dur, timeouts: to}
}

// observe records the exec's span attributes, metrics, and a finish log, and
// marks the span errored only on runErr (a started-but-cancelled or
// unstartable run). A timeout, abort, or non-zero exit is a normal outcome
// here, not a span error.
func (o *observability) observe(ctx context.Context, span trace.Span, spec CommandSpec,
	res Result, runErr error) {
	span.SetAttributes(
		attribute.Int("exit_code", res.ExitCode),
		attribute.Bool("timed_out", res.TimedOut),
		attribute.Bool("aborted", res.Aborted),
		attribute.Float64("elapsed_seconds", res.Elapsed.Seconds()),
	)
	o.execDuration.Record(ctx, res.Elapsed.Seconds())
	if res.TimedOut {
		o.timeouts.Add(ctx, 1)
	}
	if runErr != nil {
		span.RecordError(runErr)
		span.SetStatus(codes.Error, runErr.Error())
	}
	slog.DebugContext(ctx, "agent exec finished",
		slog.String("dir", spec.Dir),
		slog.Int("exit_code", res.ExitCode),
		slog.Bool("timed_out", res.TimedOut),
		slog.Bool("aborted", res.Aborted),
		slog.String("abort_reason", res.AbortReason),
		slog.Duration("elapsed", res.Elapsed),
		slog.String("stderr_tail", res.StderrTail))
}

const (
	stderrTailBytes = 4096
	maxStdoutBytes  = 32 << 20 // collection cap; the stream keeps draining past it
	waitDelay       = 5 * time.Second
)

// Result is the outcome of one agent run.
type Result struct {
	Stdout      []byte        // full stdout (bounded), ANSI-stripped
	TimedOut    bool          // the per-run timeout expired
	Aborted     bool          // the per-line observer ended the run early
	AbortReason string        // the observer's reason for aborting
	ExitCode    int           // process exit code (-1 when killed)
	StderrTail  string        // last bytes of stderr, for timeout diagnostics
	Elapsed     time.Duration // wall clock of the agent run
}

// Exec runs commands for real. Isolation is the pod's concern, not this
// package's: specs execute unconfined.
type Exec struct{}

// Run executes spec with the given timeout, collecting stdout (bounded) into
// Result.Stdout. When onLine is non-nil it observes every stdout line as it
// streams; returning abort=true kills the process group and ends the run with
// Aborted=true and the observer's reason, plus everything collected so far.
// Neither an abort nor a timed-out run is an error: a timeout returns
// TimedOut=true with whatever output arrived, so callers can grade or parse
// partial output. The returned error is non-nil only for unstartable commands
// or parent-context cancellation (Ctrl-C).
func (e *Exec) Run(ctx context.Context, spec CommandSpec, timeout time.Duration,
	onLine func(line []byte) (abort bool, reason string)) (Result, error) {
	o := obs()
	ctx, span := o.tracer.Start(ctx, "patchy.agent.exec")
	defer span.End()

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	slog.DebugContext(ctx, "agent exec started",
		slog.String("argv0", spec.Argv[0]),
		slog.String("dir", spec.Dir),
		slog.Duration("timeout", timeout))

	cmd := exec.CommandContext(runCtx, spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), spec.Env...)
	configureProcessTreeKill(cmd)
	cmd.WaitDelay = waitDelay

	stderr := &ring{max: stderrTailBytes}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, startError(span, err)
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, startError(span, err)
	}

	// Observe-AND-collect: every line is appended to the bounded collection
	// and, independently, handed to the observer. After an abort the stream
	// keeps draining to EOF so Wait can return once the group kill lands.
	var collected bytes.Buffer
	aborted, reason := false, ""
	reader := bufio.NewReader(stdout)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if collected.Len() < maxStdoutBytes {
				collected.Write(line)
			}
			if !aborted && onLine != nil {
				if abort, why := onLine(line); abort {
					aborted, reason = true, why
					cancel() // early exit; the group kill ends the stream
				}
			}
		}
		if readErr != nil {
			break // EOF or pipe closed by the kill
		}
	}
	waitErr := cmd.Wait()

	// Agents and the tools they invoke (terraform, linters, ...) emit ANSI
	// color codes; strip them here, at the one point all execution output is
	// captured, so the bytes that flow into retained logs, issue comments,
	// and reports stay plain text. Stripping is a no-op on the stream-json
	// runners emit (there a tool's ANSI sits backslash-u escaped inside a
	// JSON string, not as a raw escape byte), so parsing is unaffected.
	res := Result{
		Stdout:      stripANSI(collected.Bytes()),
		Aborted:     aborted,
		AbortReason: reason,
		StderrTail:  strings.TrimSpace(string(stripANSI(stderr.Bytes()))),
		Elapsed:     time.Since(start),
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case ctx.Err() != nil:
		o.observe(ctx, span, spec, res, ctx.Err())
		return res, ctx.Err() // interrupted from above; abort the caller's loop
	case res.Aborted:
		o.observe(ctx, span, spec, res, nil)
		return res, nil
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		o.observe(ctx, span, spec, res, nil)
		return res, nil
	default:
		// Runner exit codes are noise (headless CLIs exit non-zero on
		// max-turns, partial runs, ...); the output already tells the story.
		_ = waitErr
		o.observe(ctx, span, spec, res, nil)
		return res, nil
	}
}

// startError marks span errored for a run that never produced a Result (an
// unstartable command) and returns err unchanged.
func startError(span trace.Span, err error) error {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	return err
}

// ring keeps the last max bytes written, for stderr tails.
type ring struct {
	buf []byte
	max int
}

func (r *ring) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ring) Bytes() []byte { return r.buf }
