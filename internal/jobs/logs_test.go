// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/bitwise-media-group/patchy/internal/envelope"
)

// fakeLogs is a canned logReader; the fake clientset cannot serve custom pod
// log bodies.
type fakeLogs struct {
	body string
	err  error

	pod       string
	container string
	follow    bool
}

func (f *fakeLogs) Stream(_ context.Context, pod, container string, follow bool) (io.ReadCloser, error) {
	f.pod, f.container, f.follow = pod, container, follow
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

// jobPod is a pod whose agent container has finished — readable by Result.
func jobPod(jobName string) *corev1.Pod {
	return jobPodInState(jobName, corev1.PodSucceeded, corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{},
	})
}

func jobPodInState(jobName string, phase corev1.PodPhase, agent corev1.ContainerState) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName + "-x7k2p",
			Namespace: "patchy-agents",
			Labels:    map[string]string{"batch.kubernetes.io/job-name": jobName},
		},
		Status: corev1.PodStatus{
			Phase:             phase,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "agent", State: agent}},
		},
	}
}

// bareJob is a Job object with no terminal condition — still running.
func bareJob(jobName string) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "patchy-agents"}}
}

func eventLine(t *testing.T, e envelope.Event) string {
	t.Helper()
	line, err := e.Encode()
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	return line
}

func investigationEvent(finding string) envelope.Event {
	return envelope.Event{
		Type:    envelope.TypeInvestigation,
		Repo:    "octo/repo",
		Finding: finding,
		Investigation: &envelope.Investigation{
			Stage:          envelope.Stage{Outcome: envelope.OutcomeOK, Harness: "claude", Model: "claude-sonnet-5"},
			Recommendation: "remediate",
		},
	}
}

func remediationEvent(finding string) envelope.Event {
	return envelope.Event{
		Type:    envelope.TypeRemediation,
		Repo:    "octo/repo",
		Finding: finding,
		Remediation: &envelope.Remediation{
			Stage:   envelope.Stage{Outcome: envelope.OutcomeOK, Harness: "claude", Model: "claude-sonnet-5"},
			Success: true,
			Branch:  "patchy/" + finding,
		},
	}
}

func TestResult(t *testing.T) {
	const jobName = "patchy-def456-inv-a2"
	const finding = "finding-abc123def0-1"
	body := strings.Join([]string{
		"agent starting up",
		eventLine(t, investigationEvent(finding)),
		"PATCHY-EVENT: {not json",
		`{"v":1,"type":"investigation"}`,                                  // envelope JSON without the prefix: not an event
		"2026-07-13T10:00:00Z " + eventLine(t, remediationEvent(finding)), // runtime timestamp prefix
		"agent done",
	}, "\n") + "\n"

	cs := fake.NewClientset(jobPod(jobName))
	c := New(cs, testConfig(), nil)
	logs := &fakeLogs{body: body}
	c.logs = logs

	events, err := c.Result(context.Background(), jobName)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Result returned %d events, want 2: %+v", len(events), events)
	}
	if events[0].Type != envelope.TypeInvestigation || events[0].Finding != finding {
		t.Errorf("event[0] = %+v, want investigation for %s", events[0], finding)
	}
	if events[1].Type != envelope.TypeRemediation || events[1].Remediation == nil {
		t.Errorf("event[1] = %+v, want remediation", events[1])
	}
	if !events[1].Remediation.Success {
		t.Error("remediation event lost its payload")
	}
	if logs.container != "agent" || logs.follow {
		t.Errorf("Result streamed container %q follow=%v, want agent follow=false", logs.container, logs.follow)
	}
}

func TestResultNoPod(t *testing.T) {
	c := New(fake.NewClientset(), testConfig(), nil)
	c.logs = &fakeLogs{}
	if _, err := c.Result(context.Background(), "patchy-none-inv-a1"); err == nil {
		t.Fatal("Result without a pod succeeded, want error")
	}
}

func TestResultStreamError(t *testing.T) {
	const jobName = "patchy-abc-inv-a1"
	c := New(fake.NewClientset(jobPod(jobName)), testConfig(), nil)
	c.logs = &fakeLogs{err: errors.New("connection refused")}
	if _, err := c.Result(context.Background(), jobName); err == nil {
		t.Fatal("Result with a broken stream succeeded, want error")
	}
}

func TestResultWaitsForAgentExit(t *testing.T) {
	const jobName = "patchy-live-inv-a1"
	pod := jobPodInState(jobName, corev1.PodRunning, corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{},
	})
	c := New(fake.NewClientset(bareJob(jobName), pod), testConfig(), nil)
	logs := &fakeLogs{}
	c.logs = logs

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Result(ctx, jobName); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Result with a running agent = %v, want deadline exceeded from the wait", err)
	}
	if logs.pod != "" {
		t.Errorf("Result read the logs of %q while the agent container was still running", logs.pod)
	}
}

func TestResultAgentNeverStarted(t *testing.T) {
	const jobName = "patchy-dead-inv-a1"
	pod := jobPodInState(jobName, corev1.PodFailed, corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"},
	})
	c := New(fake.NewClientset(bareJob(jobName), pod), testConfig(), nil)
	c.logs = &fakeLogs{}

	_, err := c.Result(context.Background(), jobName)
	if err == nil || !strings.Contains(err.Error(), "never started") {
		t.Fatalf("Result on a dead pod whose agent never ran = %v, want a never-started error", err)
	}
}
