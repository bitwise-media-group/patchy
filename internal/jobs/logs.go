// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"bufio"
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/transcript"
)

// maxEventLine bounds one log line while scanning for events: a remediation
// event carries the changeset's base64 file contents (5 MiB cap → ~7 MiB
// encoded), so allow generous headroom.
const maxEventLine = 32 << 20

// logReader opens a pod's container log stream; the indirection exists so
// tests can feed canned or piped logs (the fake clientset cannot serve
// custom log bodies).
type logReader interface {
	Stream(ctx context.Context, pod, container string, follow bool) (io.ReadCloser, error)
}

// podLogs is the real logReader.
type podLogs struct {
	cs        kubernetes.Interface
	namespace string
}

func (p podLogs) Stream(ctx context.Context, pod, container string, follow bool) (io.ReadCloser, error) {
	opts := &corev1.PodLogOptions{Container: container, Follow: follow}
	return p.cs.CoreV1().Pods(p.namespace).GetLogs(pod, opts).Stream(ctx)
}

// RunOutput is everything one agent Job's log carried: the stage results the
// controller routes on, and the conversation that produced them. Both come
// from a single read — the changeset alone can be several MiB, so reading the
// log twice to separate them would be wasteful.
type RunOutput struct {
	Events []envelope.Event
	Turns  []transcript.Turn
}

// Result waits for the agent container to finish, then reads its full logs
// and returns everything found — the idempotent fallback/reconciliation path.
// Waiting for termination is what makes the read complete: a non-follow read
// of a still-running container would miss every event emitted after it.
func (c *Client) Result(ctx context.Context, jobName string) (RunOutput, error) {
	pod, err := c.waitForAgent(ctx, jobName, true)
	if err != nil {
		return RunOutput{}, err
	}
	stream, err := c.logs.Stream(ctx, pod, agentContainerName, false)
	if err != nil {
		return RunOutput{}, fmt.Errorf("jobs: read logs of %s: %w", jobName, err)
	}
	defer func() { _ = stream.Close() }()

	var out RunOutput
	err = scanLog(stream, func(e envelope.Event) error {
		out.Events = append(out.Events, e)
		return nil
	}, func(t transcript.Turn) error {
		out.Turns = append(out.Turns, t)
		return nil
	})
	if err != nil {
		return RunOutput{}, fmt.Errorf("jobs: read logs of %s: %w", jobName, err)
	}
	return out, nil
}

// scanLog splits the agent's log into its two prefixed streams, delivering
// each line to the matching handler and ignoring everything else; a handler
// error stops the scan. Either handler may be nil.
//
// Turn lines are tested first and never offered to envelope.Decode: that
// decoder searches for its prefix anywhere in the line to survive log
// wrapping, so a turn whose text quotes the envelope prefix would otherwise be
// mis-scanned as a stage result.
func scanLog(r io.Reader, onEvent func(envelope.Event) error, onTurn func(transcript.Turn) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxEventLine)
	for sc.Scan() {
		if transcript.HasPrefix(sc.Bytes()) {
			t, ok := transcript.Decode(sc.Bytes())
			if !ok || onTurn == nil {
				continue
			}
			if err := onTurn(t); err != nil {
				return err
			}
			continue
		}
		e, ok := envelope.Decode(sc.Bytes())
		if !ok || onEvent == nil {
			continue
		}
		if err := onEvent(e); err != nil {
			return err
		}
	}
	return sc.Err()
}
