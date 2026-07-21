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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// Label keys and values identifying the Jobs patchy owns. The repo
// annotation carries the true owner/name (a slash is illegal in label
// values).
const (
	labelApp       = "app.kubernetes.io/name"
	labelManagedBy = "app.kubernetes.io/managed-by"
	annotationRepo = "patchy.bitwisemedia.uk/repo"

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

// Per-Job Secret keys. No forge credential ever appears here — the init
// container fetches a digest-verified tarball from the artifact server.
const (
	secretKeyIssue         = "issue.md"
	secretKeyInvestigation = "investigation.md"
)

// runAsUser is the fixed non-root UID (distroless "nonroot").
const runAsUser = 65532

// prepareScript is the init container's shell: a credential-less fetch of
// the SHA-pinned tree tarball from source-controller's artifact server
// (digest-verified end to end), followed by a synthetic git base commit —
// the agent's commit/diff flow needs a local base, and diffs against the
// synthetic commit are identical to diffs against the real remote SHA the
// controller pushes on. No forge credential exists anywhere in the pod.
const prepareScript = `set -eu
mkdir -p /workspace/repo
cd /workspace/repo
curl -fsSL --retry 5 --retry-all-errors "$PATCHY_ARTIFACT_URL" -o /tmp/src.tar.gz
echo "$PATCHY_ARTIFACT_DIGEST  /tmp/src.tar.gz" | sha256sum -c - >/dev/null
tar -xzf /tmp/src.tar.gz --strip-components=1
rm -f /tmp/src.tar.gz
git init -q
git add -A
git -c user.name=patchy -c user.email=patchy@invalid commit -qm "base $PATCHY_BASE_SHA"
git checkout -q --detach HEAD
mkdir -p /workspace/input
cp /patchy/input/issue.md /workspace/input/issue.md
if [ -f /patchy/input/investigation.md ]; then
  cp /patchy/input/investigation.md /workspace/input/investigation.md
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
	Repo    string // "owner/name"
	Attempt int
	Phase   string // agentrun phase: "investigate" | "remediate"
	// BaseSHA is the pinned commit the artifact tree corresponds to; the
	// agent's changeset parents it.
	BaseSHA       string
	IssueMarkdown string // the issue handoff file content
	// Kind discriminates the two job controllers sharing one namespace:
	// "investigation" | "remediation".
	Kind    string
	Owner   string // owning Investigation/Remediation name
	Finding string // owning Finding name
	// ArtifactURL/ArtifactDigest locate and pin the repo tarball.
	ArtifactURL    string
	ArtifactDigest string
	// InvestigationMarkdown is the analysis handed to a remediation run.
	InvestigationMarkdown string
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

// NameFor is the deterministic Job (and per-Job Secret) name for one
// attempt: patchy-<findinghash>-{inv|rem}-a<attempt>. Always DNS-1123 safe
// and <=63 chars; the kind discriminator keeps the two job controllers
// sharing one namespace out of each other's way.
func NameFor(finding, kind string, attempt int32) string {
	sum := sha256.Sum256([]byte(finding))
	short := map[string]string{"investigation": "inv", "remediation": "rem"}[kind]
	return fmt.Sprintf("patchy-%x-%s-a%d", sum[:5], short, attempt)
}

// Create builds and creates the per-Job Secret (the handoff markdown files),
// then the Job itself, then owner-references the Secret to the Job so it is
// garbage collected with it. Returns the Job name.
func (c *Client) Create(ctx context.Context, spec Spec) (string, error) {
	if spec.Kind == "" || spec.Finding == "" {
		return "", fmt.Errorf("jobs: spec requires Kind and Finding")
	}
	name := NameFor(spec.Finding, spec.Kind, int32(spec.Attempt))
	job, err := c.buildJob(name, spec)
	if err != nil {
		return "", err
	}

	// Create is idempotent: the Secret and Job contents are deterministic
	// per (finding, kind, attempt), so a duplicate reconcile — or a retry
	// after a partial launch — adopts what already exists instead of
	// failing on AlreadyExists.
	secrets := c.cs.CoreV1().Secrets(c.cfg.Namespace)
	secret, err := secrets.Create(ctx, buildSecret(name, c.cfg.Namespace, spec), metav1.CreateOptions{})
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

	c.log.LogAttrs(ctx, slog.LevelInfo, "created agent job",
		slog.String("job", name),
		slog.String("repo", spec.Repo),
		slog.String("finding", spec.Finding),
		slog.Int("attempt", spec.Attempt))
	return name, nil
}

// buildSecret holds everything the init container needs: the handoff
// markdown files.
func buildSecret(name, namespace string, spec Spec) *corev1.Secret {
	data := map[string][]byte{
		secretKeyIssue: []byte(spec.IssueMarkdown),
	}
	if spec.InvestigationMarkdown != "" {
		data[secretKeyInvestigation] = []byte(spec.InvestigationMarkdown)
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
					InitContainers: []corev1.Container{c.prepareContainer(spec, res)},
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

// prepareContainer fetches the artifact tarball and stages the handoff
// files. No credential of any kind reaches it.
func (c *Client) prepareContainer(spec Spec, res corev1.ResourceRequirements) corev1.Container {
	env := []corev1.EnvVar{
		{Name: "HOME", Value: workspaceDir},
		{Name: "PATCHY_BASE_SHA", Value: spec.BaseSHA},
		{Name: "PATCHY_ARTIFACT_URL", Value: spec.ArtifactURL},
		{Name: "PATCHY_ARTIFACT_DIGEST", Value: spec.ArtifactDigest},
	}
	return corev1.Container{
		Name:    initContainerName,
		Image:   c.cfg.Image,
		Command: []string{"/bin/sh", "-c", prepareScript},
		Env:     env,
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
	"PATCHY_PHASE":            true,
	"PATCHY_FINDING":          true,
	"PATCHY_BASE_SHA":         true,
	"ANTHROPIC_API_KEY":       true,
	"CLAUDE_CODE_OAUTH_TOKEN": true,
	"ANTHROPIC_AUTH_TOKEN":    true,
	"GITHUB_TOKEN":            true,
}

func (c *Client) agentEnv(spec Spec) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, len(c.cfg.Env)+8)
	env = append(env,
		// HOME must be writable under readOnlyRootFilesystem.
		corev1.EnvVar{Name: "HOME", Value: workspaceDir},
		corev1.EnvVar{Name: "PATCHY_WORKSPACE", Value: workspaceDir},
		corev1.EnvVar{Name: "PATCHY_REPO", Value: spec.Repo},
		corev1.EnvVar{Name: "PATCHY_PHASE", Value: spec.Phase},
		corev1.EnvVar{Name: "PATCHY_FINDING", Value: spec.Finding},
		// The remote base SHA stamps the changeset (the local base is a
		// synthetic commit over the artifact tree).
		corev1.EnvVar{Name: "PATCHY_BASE_SHA", Value: spec.BaseSHA})

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
		labelApp:              appName,
		labelManagedBy:        managedBy,
		v1alpha1.LabelAttempt: strconv.Itoa(spec.Attempt),
		v1alpha1.LabelRunKind: spec.Kind,
		v1alpha1.LabelOwner:   sanitizeLabelValue(spec.Owner),
		v1alpha1.LabelFinding: sanitizeLabelValue(spec.Finding),
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
