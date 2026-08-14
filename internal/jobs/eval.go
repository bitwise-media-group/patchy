// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/provider"
)

// KindEvaluation is the LabelRunKind value on evaluation Jobs. It is a plain
// label value, deliberately not part of the CRD RunKind enum — the enum
// discriminates the two Finding-pipeline run kinds, and evaluation Jobs never
// touch that state machine.
const KindEvaluation = "evaluation"

// secretKeyUnit is the per-Job Secret key carrying the serialized unit spec
// the in-pod evolve client executes.
const secretKeyUnit = "unit.json"

// evalPrepareScript fetches the digest-verified workspace bundle and stages
// the unit spec. Unlike the finding prepare script there is no strip-components
// and no git init: the bundle root is the workspace shape the client built,
// and the client constructs per-case git workspaces itself.
const evalPrepareScript = `set -eu
mkdir -p /workspace/bundle /workspace/input
curl -fsSL --retry 5 --retry-all-errors "$PATCHY_ARTIFACT_URL" -o /tmp/bundle.tar.gz
echo "$PATCHY_ARTIFACT_DIGEST  /tmp/bundle.tar.gz" | sha256sum -c - >/dev/null
tar -xzf /tmp/bundle.tar.gz -C /workspace/bundle
rm -f /tmp/bundle.tar.gz
cp /patchy/input/unit.json /workspace/input/unit.json
`

// evalUnitFilePath and evalBundleDir are where the prepare container stages
// the unit inputs, announced to the agent via the EVOLVE_* env vars.
const (
	evalUnitFilePath = "/workspace/input/unit.json"
	evalBundleDir    = "/workspace/bundle"
)

// EvalSpec is one evaluation-unit Job to create.
type EvalSpec struct {
	// Evaluation is the owning Evaluation name; Unit the owning
	// EvaluationUnit (the Job's LabelOwner) and Index its position.
	Evaluation string
	Unit       string
	Index      int32
	// Harness runs this Job; selects the evolve-runner image, credential,
	// and egress network policy.
	Harness string
	// UnitJSON is the serialized unit spec handed to the pod verbatim.
	UnitJSON []byte
	// ArtifactURL/ArtifactDigest locate and pin the workspace bundle.
	ArtifactURL    string
	ArtifactDigest string
	// Deadline overrides the Config deadline for this unit (0 keeps it).
	Deadline time.Duration
}

// EvalNameFor is the deterministic Job (and per-Job Secret) name for one
// evaluation unit: patchy-<unithash>-evl. Always DNS-1123 safe and <=63
// chars; the -evl discriminator keeps evaluation Jobs out of the finding job
// controllers' way in the shared agents namespace.
func EvalNameFor(unit string) string {
	sum := sha256.Sum256([]byte(unit))
	return fmt.Sprintf("patchy-%x-evl", sum[:5])
}

// CreateEval builds and creates the per-Job Secret (the unit spec), then the
// Job, then owner-references the Secret to the Job — the same idempotent
// adopt-on-AlreadyExists flow as Create, so a duplicate reconcile resumes a
// partial launch instead of failing.
func (c *Client) CreateEval(ctx context.Context, spec EvalSpec) (string, error) {
	if spec.Evaluation == "" || spec.Unit == "" {
		return "", fmt.Errorf("jobs: eval spec requires Evaluation and Unit")
	}
	name := EvalNameFor(spec.Unit)
	job, err := c.buildEvalJob(name, spec)
	if err != nil {
		return "", err
	}

	secrets := c.cs.CoreV1().Secrets(c.cfg.Namespace)
	secret, err := secrets.Create(ctx, buildEvalSecret(name, c.cfg.Namespace, spec), metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		secret, err = secrets.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return "", fmt.Errorf("jobs: create secret %s: %w", name, err)
	}
	created, err := c.cs.BatchV1().Jobs(c.cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		created, err = c.cs.BatchV1().Jobs(c.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		_ = secrets.Delete(ctx, name, metav1.DeleteOptions{})
		return "", fmt.Errorf("jobs: create job %s: %w", name, err)
	}
	owner := metav1.OwnerReference{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Name:       created.Name,
		UID:        created.UID,
		Controller: new(true),
	}
	if !slices.ContainsFunc(secret.OwnerReferences, func(r metav1.OwnerReference) bool { return r.UID == owner.UID }) {
		secret.OwnerReferences = append(secret.OwnerReferences, owner)
		if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return "", fmt.Errorf("jobs: own secret %s: %w", name, err)
		}
	}

	c.log.LogAttrs(ctx, slog.LevelInfo, "created evaluation job",
		slog.String("job", name),
		slog.String("evaluation", spec.Evaluation),
		slog.String("unit", spec.Unit),
		slog.String("harness", spec.Harness))
	return name, nil
}

// buildEvalSecret holds the serialized unit spec the prepare container stages
// for the agent.
func buildEvalSecret(name, namespace string, spec EvalSpec) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: evalLabels(spec)},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{secretKeyUnit: spec.UnitJSON},
	}
}

func (c *Client) buildEvalJob(name string, spec EvalSpec) (*batchv1.Job, error) {
	res, err := c.cfg.resources()
	if err != nil {
		return nil, err
	}
	runner, err := c.runnerFor(spec.Harness)
	if err != nil {
		return nil, err
	}
	lbls := evalLabels(spec)

	deadline := c.cfg.Deadline
	if spec.Deadline > 0 {
		deadline = spec.Deadline
	}
	// The eval pod captures its caller token once at start (the /bin/sh
	// export wrapper), so the projection must outlive the whole run; the API
	// server caps requested expirations at 24h.
	tokenTTL := int64((deadline + time.Hour).Seconds())
	if runner.Brokered && tokenTTL >= int64((24*time.Hour).Seconds()) {
		return nil, fmt.Errorf("jobs: eval deadline %s needs a broker token TTL past the 24h projection cap", deadline)
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: c.cfg.Namespace,
			Labels:    lbls,
		},
		Spec: batchv1.JobSpec{
			// Retries are the unit reconciler's decision, never the Job
			// controller's.
			BackoffLimit: new(int32(0)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: maps.Clone(lbls)},
				Spec: corev1.PodSpec{
					ServiceAccountName: c.cfg.ServiceAccount,
					RestartPolicy:      corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   new(true),
						FSGroup:        new(int64(runAsUser)),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes:        c.podVolumes(name, runner, tokenTTL),
					InitContainers: []corev1.Container{c.evalPrepareContainer(runner, spec, res)},
					Containers:     []corev1.Container{c.evalAgentContainer(runner, res)},
				},
			},
		},
	}
	if deadline > 0 {
		job.Spec.ActiveDeadlineSeconds = new(int64(deadline.Seconds()))
	}
	if c.cfg.TTL > 0 {
		job.Spec.TTLSecondsAfterFinished = new(int32(c.cfg.TTL.Seconds()))
	}
	return job, nil
}

// evalPrepareContainer fetches the workspace bundle. No credential of any
// kind reaches it; the init only needs /bin/sh, curl, and tar.
func (c *Client) evalPrepareContainer(runner Runner, spec EvalSpec, res corev1.ResourceRequirements) corev1.Container {
	return corev1.Container{
		Name:    initContainerName,
		Image:   runner.Image,
		Command: []string{"/bin/sh", "-c", evalPrepareScript},
		Env: []corev1.EnvVar{
			{Name: "HOME", Value: workspaceDir},
			{Name: "PATCHY_ARTIFACT_URL", Value: spec.ArtifactURL},
			{Name: "PATCHY_ARTIFACT_DIGEST", Value: spec.ArtifactDigest},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: volWorkspace, MountPath: workspaceDir},
			{Name: volTmp, MountPath: "/tmp"},
			{Name: volInput, MountPath: inputMount, ReadOnly: true},
		},
		SecurityContext: containerSecurity(),
		Resources:       res,
	}
}

// evalBrokeredCommand wraps evolve exec-unit so the caller token reaches the
// CLI: eval pods run no patchy binary that could set the header per-request,
// so the wrapper captures the projected token into ANTHROPIC_CUSTOM_HEADERS
// once at start (the token TTL is sized past the deadline for exactly this).
// /bin/sh is already an image requirement — the prepare script runs it.
var evalBrokeredCommand = []string{"/bin/sh", "-c",
	`export ANTHROPIC_CUSTOM_HEADERS="` + provider.BrokerTokenHeader + `: $(cat ` + brokerTokenPath +
		`)"; exec evolve exec-unit`}

// evalAgentContainer runs the in-pod evolve client. The pod IS the sandbox:
// a non-brokered runner's only credential is the model key, injected through
// the same Secret channel as the finding runners; a brokered runner gets no
// credential at all — the gateway env plus the projected caller token.
func (c *Client) evalAgentContainer(runner Runner, res corev1.ResourceRequirements) corev1.Container {
	env := []corev1.EnvVar{
		{Name: "HOME", Value: workspaceDir},
		{Name: "EVOLVE_UNIT_FILE", Value: evalUnitFilePath},
		{Name: "EVOLVE_BUNDLE_DIR", Value: evalBundleDir},
	}
	for _, k := range slices.Sorted(maps.Keys(runner.Env)) {
		if !credentialChannelEnv[k] {
			env = append(env, corev1.EnvVar{Name: k, Value: runner.Env[k]})
		}
	}
	command := []string{"evolve", "exec-unit"}
	mounts := []corev1.VolumeMount{
		{Name: volWorkspace, MountPath: workspaceDir},
		{Name: volTmp, MountPath: "/tmp"},
	}
	switch {
	case runner.Brokered:
		command = evalBrokeredCommand
		mounts = append(mounts, corev1.VolumeMount{Name: volBrokerToken, MountPath: brokerTokenDir, ReadOnly: true})
	case runner.Secret != "":
		env = append(env, corev1.EnvVar{
			Name: runner.SecretEnv,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: runner.Secret},
					Key:                  runner.SecretKey,
				},
			},
		})
	}
	return corev1.Container{
		Name:            agentContainerName,
		Image:           runner.Image,
		Command:         command,
		Env:             env,
		VolumeMounts:    mounts,
		SecurityContext: containerSecurity(),
		Resources:       res,
	}
}

// evalLabels: the standard app labels plus the evaluation lineage. The
// harness label keeps the per-harness egress network policies applying to
// evaluation Jobs unchanged.
func evalLabels(spec EvalSpec) map[string]string {
	lbls := map[string]string{
		labelApp:                 appName,
		labelManagedBy:           managedBy,
		v1alpha1.LabelRunKind:    KindEvaluation,
		v1alpha1.LabelOwner:      sanitizeLabelValue(spec.Unit),
		v1alpha1.LabelEvaluation: sanitizeLabelValue(spec.Evaluation),
		v1alpha1.LabelUnitIndex:  strconv.Itoa(int(spec.Index)),
	}
	if spec.Harness != "" {
		lbls[v1alpha1.LabelHarness] = spec.Harness
	}
	return lbls
}
