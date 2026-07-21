// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Status reports a Job's state.
type Status struct {
	Active    int32
	Succeeded int32
	Failed    int32
	// Done means the Job reached a terminal condition (Complete or Failed).
	Done bool
}

// Status reports the named Job's state.
func (c *Client) Status(ctx context.Context, jobName string) (Status, error) {
	job, err := c.cs.BatchV1().Jobs(c.cfg.Namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return Status{}, fmt.Errorf("jobs: status of %s: %w", jobName, err)
	}
	return statusOf(job), nil
}

// Delete removes a Job and its pods (propagation: background).
func (c *Client) Delete(ctx context.Context, jobName string) error {
	policy := metav1.DeletePropagationBackground
	err := c.cs.BatchV1().Jobs(c.cfg.Namespace).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &policy,
	})
	if err != nil {
		return fmt.Errorf("jobs: delete %s: %w", jobName, err)
	}
	return nil
}

func statusOf(job *batchv1.Job) Status {
	s := Status{
		Active:    job.Status.Active,
		Succeeded: job.Status.Succeeded,
		Failed:    job.Status.Failed,
	}
	for _, cond := range job.Status.Conditions {
		terminal := cond.Type == batchv1.JobComplete || cond.Type == batchv1.JobFailed
		if terminal && cond.Status == corev1.ConditionTrue {
			s.Done = true
		}
	}
	return s
}
