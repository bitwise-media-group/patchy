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

// Runner is one harness's agent-runner deployment surface: the container image
// bundling that harness's CLI and the Secret its model credential is injected
// from. A Job picks its Runner by the harness resolved for its model, so a
// claude Job runs the claude image with the Anthropic credential and a codex
// Job the codex image with the OpenAI credential.
type Runner struct {
	Image     string // the agent-runner image bundling this harness's CLI
	Secret    string // name of the Secret holding the model credential
	SecretKey string // key within it (default "api-key")
	// SecretEnv is the env var the credential is injected into the agent
	// container as: ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN for claude,
	// OPENAI_API_KEY for codex. The fake runner needs no credential and may
	// leave Secret empty.
	SecretEnv string
}

// Config configures Job creation.
type Config struct {
	Namespace      string        // where Jobs run, e.g. "patchy-agents"
	ServiceAccount string        // pod service account
	Deadline       time.Duration // activeDeadlineSeconds
	TTL            time.Duration // ttlSecondsAfterFinished
	// Runners is the per-harness runner fleet, keyed by harness id
	// ("claude"/"codex"/"fake"). A Job whose Spec.Harness is not a key here
	// fails to build — the controller resolves and enables harnesses before a
	// Job is ever created.
	Runners map[string]Runner
	// Env is extra PATCHY_* configuration passed through to every runner
	// (models, timeouts, ceilings, thresholds). Per-Job harness and model are
	// carried on the Spec, not here.
	Env map[string]string
	// Resource strings (Kubernetes quantities), optional.
	CPURequest, MemoryRequest, CPULimit, MemoryLimit string
}

// Spec is one agent Job to create.
type Spec struct {
	Repo    string // "owner/name"
	Attempt int
	Phase   string // agentrun phase: "investigate" | "remediate"
	// Harness runs this Job ("claude"/"codex"/"fake"); selects the runner
	// image, credential, and egress network policy. Model is the canonical
	// provider-qualified model id the harness runs. Both are resolved
	// controller-side before the Job is created.
	Harness string
	Model   string
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
	runners := make(map[string]Runner, len(cfg.Runners))
	for id, r := range cfg.Runners {
		if r.SecretKey == "" {
			r.SecretKey = "api-key"
		}
		runners[id] = r
	}
	cfg.Runners = runners
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{cs: cs, cfg: cfg, log: log, logs: podLogs{cs: cs, namespace: cfg.Namespace}}
}

// runnerFor returns the runner configured for a Job's harness, or an error
// naming the harness when none is configured (a controller bug — harnesses are
// enabled at startup, before any Job is built).
func (c *Client) runnerFor(harness string) (Runner, error) {
	r, ok := c.cfg.Runners[harness]
	if !ok {
		return Runner{}, fmt.Errorf("jobs: no runner configured for harness %q", harness)
	}
	return r, nil
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
	runner, err := c.runnerFor(spec.Harness)
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
					InitContainers: []corev1.Container{c.prepareContainer(runner, spec, res)},
					Containers:     []corev1.Container{c.agentContainer(runner, spec, res)},
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
// files. No credential of any kind reaches it. It runs the same image as the
// agent container — the init only needs /bin/sh, curl, and git, which both
// runner images carry.
func (c *Client) prepareContainer(runner Runner, spec Spec, res corev1.ResourceRequirements) corev1.Container {
	env := []corev1.EnvVar{
		{Name: "HOME", Value: workspaceDir},
		{Name: "PATCHY_BASE_SHA", Value: spec.BaseSHA},
		{Name: "PATCHY_ARTIFACT_URL", Value: spec.ArtifactURL},
		{Name: "PATCHY_ARTIFACT_DIGEST", Value: spec.ArtifactDigest},
	}
	return corev1.Container{
		Name:    initContainerName,
		Image:   runner.Image,
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
func (c *Client) agentContainer(runner Runner, spec Spec, res corev1.ResourceRequirements) corev1.Container {
	return corev1.Container{
		Name:    agentContainerName,
		Image:   runner.Image,
		Command: []string{"agent-runner"},
		Env:     c.agentEnv(runner, spec),
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
// win. Every model credential channel (claude's and codex's alike) is
// reserved regardless of which one a runner injects — credentials reach the
// pod only via the secretKeyRef, never as a plaintext value in the Job spec.
// The per-Job harness/model vars are reserved too: they are resolved per Job
// and set from the Spec, so a controller-global Env copy must never shadow
// them.
var reservedEnv = map[string]bool{
	"HOME":                       true,
	"PATCHY_WORKSPACE":           true,
	"PATCHY_REPO":                true,
	"PATCHY_PHASE":               true,
	"PATCHY_FINDING":             true,
	"PATCHY_BASE_SHA":            true,
	"PATCHY_INVESTIGATE_HARNESS": true,
	"PATCHY_INVESTIGATE_MODEL":   true,
	"PATCHY_REMEDIATE_HARNESS":   true,
	"PATCHY_REMEDIATE_MODEL":     true,
	"ANTHROPIC_API_KEY":          true,
	"CLAUDE_CODE_OAUTH_TOKEN":    true,
	"ANTHROPIC_AUTH_TOKEN":       true,
	"OPENAI_API_KEY":             true,
	"GITHUB_TOKEN":               true,
}

// stageEnvNames returns the harness and model env var names agent-runner reads
// for a given phase; the controller resolves both per Job, so they are carried
// on the Spec and injected here rather than in the controller-global Env.
func stageEnvNames(phase string) (harnessEnv, modelEnv string) {
	if phase == "remediate" {
		return "PATCHY_REMEDIATE_HARNESS", "PATCHY_REMEDIATE_MODEL"
	}
	return "PATCHY_INVESTIGATE_HARNESS", "PATCHY_INVESTIGATE_MODEL"
}

func (c *Client) agentEnv(runner Runner, spec Spec) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, len(c.cfg.Env)+10)
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

	// The harness and model resolved for this Job's stage. They match the
	// runner image the pod runs in, so the agent runs the harness it was built
	// for on the model the controller chose.
	harnessEnv, modelEnv := stageEnvNames(spec.Phase)
	if spec.Harness != "" {
		env = append(env, corev1.EnvVar{Name: harnessEnv, Value: spec.Harness})
	}
	if spec.Model != "" {
		env = append(env, corev1.EnvVar{Name: modelEnv, Value: spec.Model})
	}

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

	// The fake runner needs no credential; the fixture replay authenticates
	// nothing.
	if runner.Secret == "" {
		return env
	}
	return append(env, corev1.EnvVar{Name: runner.SecretEnv, ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: runner.Secret},
			Key:                  runner.SecretKey,
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
	lbls := map[string]string{
		labelApp:              appName,
		labelManagedBy:        managedBy,
		v1alpha1.LabelAttempt: strconv.Itoa(spec.Attempt),
		v1alpha1.LabelRunKind: spec.Kind,
		v1alpha1.LabelOwner:   sanitizeLabelValue(spec.Owner),
		v1alpha1.LabelFinding: sanitizeLabelValue(spec.Finding),
	}
	// The per-harness egress network policies select agent pods by this label,
	// so each runner reaches only its own model API.
	if spec.Harness != "" {
		lbls[v1alpha1.LabelHarness] = spec.Harness
	}
	return lbls
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
