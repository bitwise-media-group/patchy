// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bitwise-media-group/patchy/internal/transcript"
)

// jobNameLabel is set on pods by the Job controller.
const jobNameLabel = "batch.kubernetes.io/job-name"

// podPollInterval paces waiting for the Job controller to create the pod.
const podPollInterval = 2 * time.Second

// Tailer reads agent Job output. It is the read-only half of Client, split out
// so a component that only watches a run — the status server following a live
// transcript — needs nothing but pod read access, and never the create/delete
// authority or the credential configuration a Client carries.
type Tailer struct {
	cs        kubernetes.Interface
	namespace string
	logs      logReader
}

// NewTailer builds a read-only reader over the agent namespace.
func NewTailer(cs kubernetes.Interface, namespace string) *Tailer {
	return &Tailer{cs: cs, namespace: namespace, logs: podLogs{cs: cs, namespace: namespace}}
}

// Tail follows a running agent's log and delivers each transcript turn as it
// is emitted, returning when the container exits, the context is cancelled, or
// fn errors. Unlike Result it waits only for the agent container to start, so
// a caller joins a run already in progress and sees the turns from that point
// on; earlier turns come from the persisted transcript instead.
//
// Envelope events are skipped: a live viewer wants the conversation, and the
// stage result is the owning controller's to apply.
func (t *Tailer) Tail(ctx context.Context, jobName string, fn func(transcript.Turn) error) error {
	pod, err := t.waitForAgent(ctx, jobName, false)
	if err != nil {
		return err
	}
	stream, err := t.logs.Stream(ctx, pod, agentContainerName, true)
	if err != nil {
		return fmt.Errorf("jobs: follow logs of %s: %w", jobName, err)
	}
	defer func() { _ = stream.Close() }()

	if err := scanLog(stream, nil, fn); err != nil {
		// A cancelled follow is the normal end of a viewer's session, not a
		// failure worth reporting up.
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("jobs: follow logs of %s: %w", jobName, err)
	}
	return nil
}

// waitForAgent polls until jobName's pod exists and its agent container has
// started — or, when terminated is true, finished — and returns the pod name.
// The wait matters: while the prepare init container clones the repo the
// agent container sits in PodInitializing, and opening its log stream then is
// rejected by the kubelet ("is waiting to start").
func (t *Tailer) waitForAgent(ctx context.Context, jobName string, terminated bool) (string, error) {
	for {
		pod, err := t.findPod(ctx, jobName)
		if err != nil {
			return "", err
		}
		switch pod {
		case nil:
			// No pod: once the Job is terminal or deleted none will appear,
			// so the logs are unrecoverable.
			gone, err := t.jobGone(ctx, jobName)
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
func (t *Tailer) jobGone(ctx context.Context, jobName string) (bool, error) {
	job, err := t.cs.BatchV1().Jobs(t.namespace).Get(ctx, jobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("jobs: status of %s: %w", jobName, err)
	}
	return statusOf(job).Done, nil
}

// findPod returns the newest pod of jobName, or nil when none exists yet.
func (t *Tailer) findPod(ctx context.Context, jobName string) (*corev1.Pod, error) {
	list, err := t.cs.CoreV1().Pods(t.namespace).List(ctx, metav1.ListOptions{
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
