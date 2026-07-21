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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// Result waits for the agent container to finish, then reads its full logs
// and returns every envelope event found — the idempotent
// fallback/reconciliation path. Waiting for termination is what makes the
// read complete: a non-follow read of a still-running container would miss
// every event emitted after it.
func (c *Client) Result(ctx context.Context, jobName string) ([]envelope.Event, error) {
	pod, err := c.waitForAgent(ctx, jobName, true)
	if err != nil {
		return nil, err
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

// waitForAgent polls until jobName's pod exists and its agent container has
// started — or, when terminated is true, finished — and returns the pod name.
// The wait matters: while the prepare init container clones the repo the
// agent container sits in PodInitializing, and opening its log stream then is
// rejected by the kubelet ("is waiting to start").
func (c *Client) waitForAgent(ctx context.Context, jobName string, terminated bool) (string, error) {
	for {
		pod, err := c.findPod(ctx, jobName)
		if err != nil {
			return "", err
		}
		switch pod {
		case nil:
			// No pod: once the Job is terminal or deleted none will appear,
			// so the logs are unrecoverable.
			gone, err := c.jobGone(ctx, jobName)
			if err != nil {
				return "", err
			}
			if gone {
				return "", fmt.Errorf("jobs: no pods for job %s", jobName)
			}
		default:
			agent := agentStatus(pod)
			started := agent != nil && agent.State.Waiting == nil
			finished := agent != nil && agent.State.Terminated != nil
			if finished || (started && !terminated) {
				return pod.Name, nil
			}
			if terminal := pod.Status.Phase == corev1.PodSucceeded ||
				pod.Status.Phase == corev1.PodFailed; terminal && !started {
				// The pod died before the agent ever ran (init failure,
				// deadline kill, eviction) — there are no logs to read.
				reason := "no status"
				if agent != nil && agent.State.Waiting != nil && agent.State.Waiting.Reason != "" {
					reason = agent.State.Waiting.Reason
				}
				return "", fmt.Errorf("jobs: agent container of %s never started (pod phase %s, %s)",
					jobName, pod.Status.Phase, reason)
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(podPollInterval):
		}
	}
}

// jobGone reports whether jobName can no longer produce a pod: it is deleted
// or already terminal.
func (c *Client) jobGone(ctx context.Context, jobName string) (bool, error) {
	job, err := c.cs.BatchV1().Jobs(c.cfg.Namespace).Get(ctx, jobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("jobs: status of %s: %w", jobName, err)
	}
	return statusOf(job).Done, nil
}

// agentStatus returns the agent container's status, or nil before the kubelet
// has reported one.
func agentStatus(pod *corev1.Pod) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == agentContainerName {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

// findPod returns the newest pod of jobName, or nil when none exists yet.
func (c *Client) findPod(ctx context.Context, jobName string) (*corev1.Pod, error) {
	list, err := c.cs.CoreV1().Pods(c.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: jobNameLabel + "=" + jobName,
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: list pods of %s: %w", jobName, err)
	}
	var newest *corev1.Pod
	for i := range list.Items {
		pod := &list.Items[i]
		if newest == nil || newest.CreationTimestamp.Before(&pod.CreationTimestamp) {
			newest = pod
		}
	}
	return newest, nil
}
