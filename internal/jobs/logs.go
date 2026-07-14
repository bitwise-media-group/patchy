// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bitwise-media-group/patchy/internal/envelope"
)

// jobNameLabel is set on pods by the Job controller.
const jobNameLabel = "batch.kubernetes.io/job-name"

// maxEventLine bounds one log line while scanning for events: a remediation
// event carries the changeset's base64 file contents (5 MiB cap → ~7 MiB
// encoded), so allow generous headroom.
const maxEventLine = 32 << 20

// podPollInterval paces waiting for the Job controller to create the pod.
const podPollInterval = 2 * time.Second

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

// Follow streams the agent container's stdout, decoding envelope events and
// delivering them to fn as they arrive. It returns when the pod's log stream
// ends. If the log stream cannot be opened or breaks, the caller falls back
// to Result.
func (c *Client) Follow(ctx context.Context, jobName string, fn func(envelope.Event) error) error {
	pod, err := c.waitForPod(ctx, jobName)
	if err != nil {
		return err
	}
	stream, err := c.logs.Stream(ctx, pod, agentContainerName, true)
	if err != nil {
		return fmt.Errorf("jobs: follow %s: %w", jobName, err)
	}
	defer func() { _ = stream.Close() }()
	return scanEvents(stream, fn)
}

// Result reads the completed Job's full logs and returns every envelope
// event found — the idempotent fallback/reconciliation path.
func (c *Client) Result(ctx context.Context, jobName string) ([]envelope.Event, error) {
	pod, err := c.findPod(ctx, jobName)
	if err != nil {
		return nil, err
	}
	if pod == "" {
		return nil, fmt.Errorf("jobs: no pods for job %s", jobName)
	}
	stream, err := c.logs.Stream(ctx, pod, agentContainerName, false)
	if err != nil {
		return nil, fmt.Errorf("jobs: read logs of %s: %w", jobName, err)
	}
	defer func() { _ = stream.Close() }()

	var events []envelope.Event
	err = scanEvents(stream, func(e envelope.Event) error {
		events = append(events, e)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: read logs of %s: %w", jobName, err)
	}
	return events, nil
}

// scanEvents delivers every envelope event in r to fn, ignoring all other
// lines; an fn error stops the scan.
func scanEvents(r io.Reader, fn func(envelope.Event) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxEventLine)
	for sc.Scan() {
		e, ok := envelope.Decode(sc.Bytes())
		if !ok {
			continue
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return sc.Err()
}

// waitForPod polls until the Job controller has created a pod for jobName.
func (c *Client) waitForPod(ctx context.Context, jobName string) (string, error) {
	for {
		pod, err := c.findPod(ctx, jobName)
		if err != nil || pod != "" {
			return pod, err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(podPollInterval):
		}
	}
}

// findPod returns the newest pod of jobName, or "" when none exists yet.
func (c *Client) findPod(ctx context.Context, jobName string) (string, error) {
	list, err := c.cs.CoreV1().Pods(c.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: jobNameLabel + "=" + jobName,
	})
	if err != nil {
		return "", fmt.Errorf("jobs: list pods of %s: %w", jobName, err)
	}
	var newest *corev1.Pod
	for i := range list.Items {
		pod := &list.Items[i]
		if newest == nil || newest.CreationTimestamp.Before(&pod.CreationTimestamp) {
			newest = pod
		}
	}
	if newest == nil {
		return "", nil
	}
	return newest.Name, nil
}
