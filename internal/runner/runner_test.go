// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package runner

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func sh(script string) CommandSpec {
	return CommandSpec{Argv: []string{"/bin/sh", "-c", script}}
}

func TestRunCollectsStdout(t *testing.T) {
	res, err := (&Exec{}).Run(context.Background(), sh(`printf 'line1\nline2\n'`), 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Stdout); got != "line1\nline2\n" {
		t.Errorf("stdout = %q", got)
	}
	if res.TimedOut || res.Aborted {
		t.Errorf("res = %+v, want clean run", res)
	}
}

func TestRunObserveAndCollect(t *testing.T) {
	// The observer must see every line while stdout is still collected in
	// full — observation is a tap on the stream, not a mode switch.
	var observed [][]byte
	res, err := (&Exec{}).Run(context.Background(), sh(`printf 'a\nb\nc\n'`), 5*time.Second,
		func(line []byte) (bool, string) {
			observed = append(observed, bytes.Clone(line))
			return false, ""
		})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Stdout); got != "a\nb\nc\n" {
		t.Errorf("stdout = %q, want full output alongside observation", got)
	}
	if got := string(bytes.Join(observed, nil)); got != "a\nb\nc\n" {
		t.Errorf("observed = %q, want every line", got)
	}
	if res.Aborted {
		t.Errorf("res = %+v, want no abort", res)
	}
}

func TestRunAbortKillsProcessGroup(t *testing.T) {
	// The process would sleep for 60s after the second line; an aborting
	// observer must kill it as soon as that line streams, keeping everything
	// collected so far and the observer's reason. An abort is not an error.
	spec := sh(`echo one; echo two; sleep 60; echo late`)
	start := time.Now()
	res, err := (&Exec{}).Run(context.Background(), spec, 30*time.Second,
		func(line []byte) (bool, string) {
			if bytes.Contains(line, []byte("two")) {
				return true, "output-token budget exceeded"
			}
			return false, ""
		})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Aborted || res.AbortReason != "output-token budget exceeded" {
		t.Errorf("res = %+v, want Aborted with the observer's reason", res)
	}
	if res.TimedOut {
		t.Error("abort must not be reported as a timeout")
	}
	if got := string(res.Stdout); !strings.Contains(got, "one\ntwo\n") || strings.Contains(got, "late") {
		t.Errorf("stdout = %q, want everything up to the abort and nothing after", got)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("abort kill took %s, want well under the sleep", elapsed)
	}
}

func TestRunSurfacesExitCode(t *testing.T) {
	// A non-zero exit is data the harness runtime-error classifier relies on,
	// not an error the runner should swallow or return.
	res, err := (&Exec{}).Run(context.Background(), sh(`echo out; exit 7`), 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
	if got := string(res.Stdout); got != "out\n" {
		t.Errorf("stdout = %q, want %q", got, "out\n")
	}
}

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	// The child ignores nothing, but it is a *grand*child via the subshell —
	// only a process-group kill reaps it promptly. Timeout keeps the partial
	// output and is not an error.
	spec := sh(`(sleep 60 & echo started; wait)`)
	start := time.Now()
	res, err := (&Exec{}).Run(context.Background(), spec, 500*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Error("want TimedOut")
	}
	if got := string(res.Stdout); !strings.Contains(got, "started") {
		t.Errorf("partial stdout = %q, want the pre-timeout output", got)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("timeout kill took %s; grandchild likely held the pipe", elapsed)
	}
}

func TestRunStderrTailOnTimeout(t *testing.T) {
	spec := sh(`echo "rate limited by upstream" >&2; sleep 60`)
	res, err := (&Exec{}).Run(context.Background(), spec, 500*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || !strings.Contains(res.StderrTail, "rate limited") {
		t.Errorf("res = %+v, want stderr tail with the message", res)
	}
}

func TestRunStripsANSIEscapes(t *testing.T) {
	// Agents and the tools they invoke (terraform, linters, ...) print ANSI
	// color codes; the runner strips them at capture so the bytes that reach
	// retained logs and reports stay plain text. The printf octal \033 emits
	// a real escape byte on both stdout and stderr.
	spec := sh(`printf '\033[31mred\033[0m plain\n'; printf '\033[1mbold\033[0m err\n' >&2`)
	res, err := (&Exec{}).Run(context.Background(), spec, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Stdout); got != "red plain\n" {
		t.Errorf("stdout = %q, want ANSI stripped", got)
	}
	if got := res.StderrTail; got != "bold err" {
		t.Errorf("stderrTail = %q, want ANSI stripped", got)
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "hello\n", "hello\n"},
		{"csi color", "\x1b[31mred\x1b[0m", "red"},
		{"csi cursor", "a\x1b[2Kb", "ab"},
		{"osc bel", "\x1b]0;title\x07text", "text"},
		{"osc st", "\x1b]8;;http://x\x1b\\link", "link"},
		{"charset", "\x1b(Bok", "ok"},
		{"trailing esc", "end\x1b", "end"},
	}
	for _, tt := range tests {
		if got := string(stripANSI([]byte(tt.in))); got != tt.want {
			t.Errorf("%s: stripANSI(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestRunParentCancelPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := (&Exec{}).Run(ctx, sh(`sleep 60`), 30*time.Second, nil)
	if err == nil {
		t.Error("want error on parent cancellation (Ctrl-C must abort the caller's loop)")
	}
}

func TestRunMissingBinary(t *testing.T) {
	spec := CommandSpec{Argv: []string{"/nonexistent/definitely-missing"}}
	if _, err := (&Exec{}).Run(context.Background(), spec, time.Second, nil); err == nil {
		t.Error("want error for missing binary")
	}
}

func TestRunLongLines(t *testing.T) {
	// stream-json lines can exceed bufio.Scanner's 64 KiB default; the reader
	// must hand them to the observer intact and collect them in full.
	spec := sh(`awk 'BEGIN{ s="x"; for (i=0; i<17; i++) s = s s; print s "NEEDLE" }'`)
	seen := false
	res, err := (&Exec{}).Run(context.Background(), spec, 10*time.Second,
		func(line []byte) (bool, string) {
			if bytes.Contains(line, []byte("NEEDLE")) {
				seen = true
			}
			return false, ""
		})
	if err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Error("needle at the end of a 128 KiB line was not observed")
	}
	if !bytes.Contains(res.Stdout, []byte("NEEDLE")) {
		t.Error("needle at the end of a 128 KiB line was not collected")
	}
}
