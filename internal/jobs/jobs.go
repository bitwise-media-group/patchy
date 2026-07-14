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
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Label keys and values identifying the Jobs patchy owns. The repo label is
// sanitized (owner/name has a slash, which label values forbid); the
// annotation of the same key carries the true owner/name.
const (
	labelApp       = "app.kubernetes.io/name"
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelIssue     = "patchy.io/issue"
	labelAttempt   = "patchy.io/attempt"
	labelRepo      = "patchy.io/repo"
	annotationRepo = "patchy.io/repo"

	appName   = "patchy-agent"
	managedBy = "patchy"
)

// Pod layout: container, volume, and mount names.
const (
	initContainerName  = "prepare"
	agentContainerName = "agent"

	volWorkspace = "workspace"
	volTmp       = "tmp"
	volInput     = "input"

	workspaceDir = "/workspace"
	inputMount   = "/patchy/input"
)

// Per-Job Secret keys.
const (
	secretKeyToken          = "token"
	secretKeyIssue          = "issue.md"
	secretKeyClassification = "classification.md"
)

// runAsUser is the fixed non-root UID (distroless "nonroot").
const runAsUser = 65532

// prepareScript is the init container's shell. The GitHub token arrives only
// as $GITHUB_TOKEN (secretKeyRef env); the auth header is assembled inside
// the shell so the plaintext token never appears in the container command,
// and the credential never touches the remote URL or any config that
// persists into the clone the agent container sees. Clone URL and base SHA
// arrive as env too, so nothing from the issue is interpolated into the
// script. The fetch is pinned to the exact base SHA the controller resolved
// (tag-free, depth 1), so the agent's diff base is deterministic no matter
// how the default branch moves.
const prepareScript = `set -eu
auth=$(printf '%s' "x-access-token:$GITHUB_TOKEN" | base64 | tr -d '\n')
mkdir -p /workspace/repo
cd /workspace/repo
git init -q
git remote add origin "$PATCHY_CLONE_URL"
git -c http.extraHeader="AUTHORIZATION: basic $auth" \
  fetch -q --depth 1 --no-tags origin "$PATCHY_BASE_SHA"
git checkout -q --detach FETCH_HEAD
unset auth GITHUB_TOKEN
mkdir -p /workspace/input
cp /patchy/input/issue.md /workspace/input/issue.md
if [ -f /patchy/input/classification.md ]; then
  cp /patchy/input/classification.md /workspace/input/classification.md
fi
`

// Config configures Job creation.
type Config struct {
	Namespace          string        // where Jobs run, e.g. "patchy-agents"
	Image              string        // agent-runner image
	ServiceAccount     string        // pod service account
	Deadline           time.Duration // activeDeadlineSeconds
	TTL                time.Duration // ttlSecondsAfterFinished
	AnthropicSecret    string        // name of the Secret holding the model credential
	AnthropicSecretKey string        // key within it (default "api-key")
	// AnthropicSecretEnv is the env var the credential is injected into the
	// agent container as: ANTHROPIC_API_KEY (the default) for an API key, or
	// CLAUDE_CODE_OAUTH_TOKEN for a `claude setup-token` OAuth token.
	AnthropicSecretEnv string
	// Env is extra PATCHY_* configuration passed through to every runner
	// (models, timeouts, ceilings, thresholds, harness selection).
	Env map[string]string
	// Resource strings (Kubernetes quantities), optional.
	CPURequest, MemoryRequest, CPULimit, MemoryLimit string
}

// Spec is one agent Job to create.
type Spec struct {
	Repo     string // "owner/name"
	Issue    int
	Attempt  int
	Phase    string // agentrun phase: "classify+remediate" | "remediate"
	CloneURL string // https URL of the repo
	// BaseSHA is the default branch's head at Job creation; the clone is
	// pinned to it and the agent's commit parents it.
	BaseSHA string
	// Token is the short-lived scoped GitHub token; it reaches the init
	// container only, via the per-Job Secret.
	Token         string
	IssueMarkdown string // the issue handoff file content
	// ClassificationMarkdown is optional: the /approve remediate-only re-run.
	ClassificationMarkdown string
}

// Client creates and observes agent Jobs in one namespace.
type Client struct {
	cs   kubernetes.Interface
	cfg  Config
	log  *slog.Logger
	logs logReader
}

// New builds a Client, applying Config defaults.
func New(cs kubernetes.Interface, cfg Config, log *slog.Logger) *Client {
	if cfg.AnthropicSecretKey == "" {
		cfg.AnthropicSecretKey = "api-key"
	}
	if cfg.AnthropicSecretEnv == "" {
		cfg.AnthropicSecretEnv = "ANTHROPIC_API_KEY"
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{cs: cs, cfg: cfg, log: log, logs: podLogs{cs: cs, namespace: cfg.Namespace}}
}

// Name is the deterministic Job (and per-Job Secret) name for one attempt:
// patchy-<repohash>-i<issue>-a<attempt>. Always DNS-1123 safe and <=63 chars
// — the repo appears only as a hash, the true owner/name lives in the
// annotation.
func Name(repo string, issue, attempt int) string {
	sum := sha256.Sum256([]byte(repo))
	return fmt.Sprintf("patchy-%x-i%d-a%d", sum[:5], issue, attempt)
}

// Create builds and creates the per-Job Secret (token + issue markdown),
// then the Job itself, then owner-references the Secret to the Job so it is
// garbage collected with it. Returns the Job name.
func (c *Client) Create(ctx context.Context, spec Spec) (string, error) {
	name := Name(spec.Repo, spec.Issue, spec.Attempt)
	job, err := c.buildJob(name, spec)
	if err != nil {
		return "", err
	}

	secrets := c.cs.CoreV1().Secrets(c.cfg.Namespace)
	secret, err := secrets.Create(ctx, buildSecret(name, c.cfg.Namespace, spec), metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("jobs: create secret %s: %w", name, err)
	}
	created, err := c.cs.BatchV1().Jobs(c.cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		_ = secrets.Delete(ctx, name, metav1.DeleteOptions{})
		return "", fmt.Errorf("jobs: create job %s: %w", name, err)
	}
	secret.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Name:       created.Name,
		UID:        created.UID,
		Controller: new(true),
	}}
	if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("jobs: own secret %s: %w", name, err)
	}

	c.log.LogAttrs(ctx, slog.LevelInfo, "created agent job",
		slog.String("job", name),
		slog.String("repo", spec.Repo),
		slog.Int("issue", spec.Issue),
		slog.Int("attempt", spec.Attempt))
	return name, nil
}

// buildSecret holds everything the init container needs: the GitHub token
// and the handoff markdown files.
func buildSecret(name, namespace string, spec Spec) *corev1.Secret {
	data := map[string][]byte{
		secretKeyToken: []byte(spec.Token),
		secretKeyIssue: []byte(spec.IssueMarkdown),
	}
	if spec.ClassificationMarkdown != "" {
		data[secretKeyClassification] = []byte(spec.ClassificationMarkdown)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: jobLabels(spec)},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
}

func (c *Client) buildJob(name string, spec Spec) (*batchv1.Job, error) {
	res, err := c.cfg.resources()
	if err != nil {
		return nil, err
	}
	lbls := jobLabels(spec)
	ann := map[string]string{annotationRepo: spec.Repo}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   c.cfg.Namespace,
			Labels:      lbls,
			Annotations: maps.Clone(ann),
		},
		Spec: batchv1.JobSpec{
			// Retries are the issue state machine's job, not the Job
			// controller's.
			BackoffLimit: new(int32(0)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: maps.Clone(lbls), Annotations: maps.Clone(ann)},
				Spec: corev1.PodSpec{
					ServiceAccountName: c.cfg.ServiceAccount,
					RestartPolicy:      corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   new(true),
						FSGroup:        new(int64(runAsUser)),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes:        volumes(name),
					InitContainers: []corev1.Container{c.prepareContainer(name, spec, res)},
					Containers:     []corev1.Container{c.agentContainer(spec, res)},
				},
			},
		},
	}
	if c.cfg.Deadline > 0 {
		job.Spec.ActiveDeadlineSeconds = new(int64(c.cfg.Deadline.Seconds()))
	}
	if c.cfg.TTL > 0 {
		job.Spec.TTLSecondsAfterFinished = new(int32(c.cfg.TTL.Seconds()))
	}
	return job, nil
}

// volumes: the shared workspace, a writable /tmp (the root filesystem is
// read-only), and the per-Job Secret for the init container.
func volumes(secretName string) []corev1.Volume {
	return []corev1.Volume{
		{Name: volWorkspace, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: volTmp, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: volInput, VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secretName},
		}},
	}
}

// prepareContainer clones the repo and stages the handoff files. It is the
// only container that can see the GitHub token.
func (c *Client) prepareContainer(secretName string, spec Spec, res corev1.ResourceRequirements) corev1.Container {
	return corev1.Container{
		Name:    initContainerName,
		Image:   c.cfg.Image,
		Command: []string{"/bin/sh", "-c", prepareScript},
		Env: []corev1.EnvVar{
			{Name: "HOME", Value: workspaceDir},
			{Name: "PATCHY_CLONE_URL", Value: spec.CloneURL},
			{Name: "PATCHY_BASE_SHA", Value: spec.BaseSHA},
			{Name: "GITHUB_TOKEN", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  secretKeyToken,
				},
			}},
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

// agentContainer runs agent-runner. No GitHub credential reaches it — that
// is the isolation model.
func (c *Client) agentContainer(spec Spec, res corev1.ResourceRequirements) corev1.Container {
	return corev1.Container{
		Name:    agentContainerName,
		Image:   c.cfg.Image,
		Command: []string{"agent-runner"},
		Env:     c.agentEnv(spec),
		VolumeMounts: []corev1.VolumeMount{
			{Name: volWorkspace, MountPath: workspaceDir},
			{Name: volTmp, MountPath: "/tmp"},
		},
		SecurityContext: containerSecurity(),
		Resources:       res,
	}
}

// reservedEnv are the names Create owns; Config.Env entries with these names
// are ignored so per-Job values (and the no-GitHub-token invariant) always
// win. Every claude credential channel is reserved regardless of which one
// AnthropicSecretEnv selects — credentials reach the pod only via the
// secretKeyRef, never as a plaintext value in the Job spec.
var reservedEnv = map[string]bool{
	"HOME":                    true,
	"PATCHY_WORKSPACE":        true,
	"PATCHY_REPO":             true,
	"PATCHY_ISSUE":            true,
	"PATCHY_PHASE":            true,
	"ANTHROPIC_API_KEY":       true,
	"CLAUDE_CODE_OAUTH_TOKEN": true,
	"ANTHROPIC_AUTH_TOKEN":    true,
	"GITHUB_TOKEN":            true,
}

func (c *Client) agentEnv(spec Spec) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, len(c.cfg.Env)+6)
	env = append(env,
		// HOME must be writable under readOnlyRootFilesystem.
		corev1.EnvVar{Name: "HOME", Value: workspaceDir},
		corev1.EnvVar{Name: "PATCHY_WORKSPACE", Value: workspaceDir},
		corev1.EnvVar{Name: "PATCHY_REPO", Value: spec.Repo},
		corev1.EnvVar{Name: "PATCHY_ISSUE", Value: strconv.Itoa(spec.Issue)},
		corev1.EnvVar{Name: "PATCHY_PHASE", Value: spec.Phase})

	keys := make([]string, 0, len(c.cfg.Env))
	for k := range c.cfg.Env {
		if !reservedEnv[k] {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		env = append(env, corev1.EnvVar{Name: k, Value: c.cfg.Env[k]})
	}

	return append(env, corev1.EnvVar{Name: c.cfg.AnthropicSecretEnv, ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: c.cfg.AnthropicSecret},
			Key:                  c.cfg.AnthropicSecretKey,
		},
	}})
}

func containerSecurity() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsNonRoot:             new(true),
		RunAsUser:                new(int64(runAsUser)),
		AllowPrivilegeEscalation: new(false),
		ReadOnlyRootFilesystem:   new(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func jobLabels(spec Spec) map[string]string {
	return map[string]string{
		labelApp:       appName,
		labelManagedBy: managedBy,
		labelIssue:     strconv.Itoa(spec.Issue),
		labelAttempt:   strconv.Itoa(spec.Attempt),
		labelRepo:      sanitizeLabelValue(spec.Repo),
	}
}

// sanitizeLabelValue coerces owner/name into a legal label value: lowercase
// [a-z0-9-._], <=63 chars, alphanumeric at both ends.
func sanitizeLabelValue(s string) string {
	b := []byte(strings.ToLower(s))
	for i, ch := range b {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-', ch == '.', ch == '_':
		default:
			b[i] = '-'
		}
	}
	out := string(b)
	if len(out) > 63 {
		out = out[:63]
	}
	out = strings.Trim(out, "-._")
	if out == "" {
		return "unknown"
	}
	return out
}

func (c Config) resources() (corev1.ResourceRequirements, error) {
	var rr corev1.ResourceRequirements
	var err error
	if rr.Requests, err = resourceList(c.CPURequest, c.MemoryRequest); err != nil {
		return rr, err
	}
	rr.Limits, err = resourceList(c.CPULimit, c.MemoryLimit)
	return rr, err
}

func resourceList(cpu, memory string) (corev1.ResourceList, error) {
	if cpu == "" && memory == "" {
		return nil, nil
	}
	rl := corev1.ResourceList{}
	if cpu != "" {
		q, err := resource.ParseQuantity(cpu)
		if err != nil {
			return nil, fmt.Errorf("jobs: cpu quantity %q: %w", cpu, err)
		}
		rl[corev1.ResourceCPU] = q
	}
	if memory != "" {
		q, err := resource.ParseQuantity(memory)
		if err != nil {
			return nil, fmt.Errorf("jobs: memory quantity %q: %w", memory, err)
		}
		rl[corev1.ResourceMemory] = q
	}
	return rl, nil
}
