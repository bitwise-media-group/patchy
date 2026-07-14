// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func testConfig() Config {
	return Config{
		Namespace:       "patchy-agents",
		Image:           "ghcr.io/bitwise-media-group/patchy-agent:1",
		ServiceAccount:  "patchy-agent",
		Deadline:        time.Hour,
		TTL:             2 * time.Hour,
		AnthropicSecret: "anthropic",
		Env: map[string]string{
			"PATCHY_CLASSIFY_MODEL":   "claude-sonnet-5",
			"PATCHY_CLASSIFY_TIMEOUT": "15m",
			"GITHUB_TOKEN":            "must-never-pass-through",
			"CLAUDE_CODE_OAUTH_TOKEN": "must-never-pass-through",
		},
		CPURequest:    "500m",
		MemoryRequest: "1Gi",
		CPULimit:      "2",
		MemoryLimit:   "4Gi",
	}
}

func testSpec() Spec {
	return Spec{
		Repo:          "octo/repo",
		Issue:         42,
		Attempt:       1,
		Phase:         "classify+remediate",
		CloneURL:      "https://github.com/octo/repo.git",
		BaseSHA:       "0123456789abcdef0123456789abcdef01234567",
		Token:         "ghs_secret_token_value",
		IssueMarkdown: "# Issue 42\n",
	}
}

func TestName(t *testing.T) {
	tests := []struct {
		name           string
		repoA, repoB   string
		issueA, issueB int
		attA, attB     int
		wantEqual      bool
	}{
		{"same inputs", "octo/repo", "octo/repo", 42, 42, 1, 1, true},
		{"different repo", "octo/repo", "octo/other", 42, 42, 1, 1, false},
		{"different issue", "octo/repo", "octo/repo", 42, 43, 1, 1, false},
		{"different attempt", "octo/repo", "octo/repo", 42, 42, 1, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := Name(tt.repoA, tt.issueA, tt.attA)
			b := Name(tt.repoB, tt.issueB, tt.attB)
			if (a == b) != tt.wantEqual {
				t.Errorf("Name equality = %v (%q vs %q), want %v", a == b, a, b, tt.wantEqual)
			}
		})
	}
}

func TestNameShape(t *testing.T) {
	long := strings.Repeat("a", 300) + "/" + strings.Repeat("b", 300)
	tests := []struct {
		name string
		repo string
	}{
		{"short repo", "octo/repo"},
		{"long repo", long},
		{"uppercase repo", "Octo/RePo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Name(tt.repo, 123456, 99)
			if len(got) > 63 {
				t.Errorf("Name(%q) = %q: %d chars, want <= 63", tt.repo, got, len(got))
			}
			if !dns1123.MatchString(got) {
				t.Errorf("Name(%q) = %q is not DNS-1123 safe", tt.repo, got)
			}
			if !strings.HasPrefix(got, "patchy-") {
				t.Errorf("Name(%q) = %q, want patchy- prefix", tt.repo, got)
			}
		})
	}
}

func TestCreateJobShape(t *testing.T) {
	cs := fake.NewClientset()
	c := New(cs, testConfig(), nil)
	spec := testSpec()

	name, err := c.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if want := Name(spec.Repo, spec.Issue, spec.Attempt); name != want {
		t.Fatalf("Create returned %q, want deterministic %q", name, want)
	}

	job, err := cs.BatchV1().Jobs("patchy-agents").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	wantLabels := map[string]string{
		"app.kubernetes.io/name":       "patchy-agent",
		"app.kubernetes.io/managed-by": "patchy",
		"patchy.io/issue":              "42",
		"patchy.io/attempt":            "1",
		"patchy.io/repo":               "octo-repo",
	}
	for _, lbls := range []map[string]string{job.Labels, job.Spec.Template.Labels} {
		for k, want := range wantLabels {
			if got := lbls[k]; got != want {
				t.Errorf("label %s = %q, want %q", k, got, want)
			}
		}
	}
	if got := job.Annotations["patchy.io/repo"]; got != "octo/repo" {
		t.Errorf("annotation patchy.io/repo = %q, want octo/repo", got)
	}

	if got := *job.Spec.BackoffLimit; got != 0 {
		t.Errorf("backoffLimit = %d, want 0", got)
	}
	if got := *job.Spec.ActiveDeadlineSeconds; got != 3600 {
		t.Errorf("activeDeadlineSeconds = %d, want 3600", got)
	}
	if got := *job.Spec.TTLSecondsAfterFinished; got != 7200 {
		t.Errorf("ttlSecondsAfterFinished = %d, want 7200", got)
	}
	pod := job.Spec.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %q, want Never", pod.RestartPolicy)
	}
	if pod.ServiceAccountName != "patchy-agent" {
		t.Errorf("serviceAccountName = %q, want patchy-agent", pod.ServiceAccountName)
	}
	if pod.SecurityContext == nil || pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != 65532 {
		t.Errorf("pod fsGroup = %+v, want 65532", pod.SecurityContext)
	}
	if len(pod.InitContainers) != 1 || pod.InitContainers[0].Name != "prepare" {
		t.Fatalf("init containers = %+v, want one named prepare", pod.InitContainers)
	}
	if len(pod.Containers) != 1 || pod.Containers[0].Name != "agent" {
		t.Fatalf("containers = %+v, want one named agent", pod.Containers)
	}
}

func TestCreatePinnedClone(t *testing.T) {
	cs := fake.NewClientset()
	c := New(cs, testConfig(), nil)
	spec := testSpec()

	name, err := c.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	job, err := cs.BatchV1().Jobs("patchy-agents").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	prepare := job.Spec.Template.Spec.InitContainers[0]
	script := strings.Join(prepare.Command, " ")

	// The clone is pinned: a tag-free depth-1 fetch of the exact base SHA,
	// never a moving branch ref.
	if !strings.Contains(script, `fetch -q --depth 1 --no-tags origin "$PATCHY_BASE_SHA"`) {
		t.Errorf("prepare script does not fetch the pinned base SHA:\n%s", script)
	}
	if strings.Contains(script, "--branch") || strings.Contains(script, "git clone") {
		t.Errorf("prepare script still clones a branch ref:\n%s", script)
	}
	if !strings.Contains(script, "checkout -q --detach FETCH_HEAD") {
		t.Errorf("prepare script does not check out FETCH_HEAD:\n%s", script)
	}

	envs := map[string]string{}
	for _, env := range prepare.Env {
		envs[env.Name] = env.Value
	}
	if envs["PATCHY_BASE_SHA"] != spec.BaseSHA {
		t.Errorf("init PATCHY_BASE_SHA = %q, want %q", envs["PATCHY_BASE_SHA"], spec.BaseSHA)
	}
	// The agent recovers the base from HEAD; the SHA env stays init-only.
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "PATCHY_BASE_SHA" {
			t.Error("agent container carries PATCHY_BASE_SHA; it must be init-only")
		}
	}
}

func TestCreateTokenIsolation(t *testing.T) {
	cs := fake.NewClientset()
	c := New(cs, testConfig(), nil)
	spec := testSpec()

	name, err := c.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	job, err := cs.BatchV1().Jobs("patchy-agents").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	prepare := job.Spec.Template.Spec.InitContainers[0]
	agent := job.Spec.Template.Spec.Containers[0]

	// The init container gets the token via secretKeyRef env only — never
	// as a literal value or on the command line.
	var tokenEnv *corev1.EnvVar
	for i, env := range prepare.Env {
		if env.Name == "GITHUB_TOKEN" {
			tokenEnv = &prepare.Env[i]
		}
		if strings.Contains(env.Value, spec.Token) {
			t.Errorf("init env %s carries the token as a literal value", env.Name)
		}
	}
	if tokenEnv == nil {
		t.Fatal("init container has no GITHUB_TOKEN env")
	}
	ref := tokenEnv.ValueFrom.SecretKeyRef
	if ref == nil || ref.Name != name || ref.Key != "token" {
		t.Errorf("GITHUB_TOKEN secretKeyRef = %+v, want secret %q key token", tokenEnv.ValueFrom, name)
	}
	if cmd := strings.Join(prepare.Command, " "); strings.Contains(cmd, spec.Token) {
		t.Error("init container command contains the plaintext token")
	}

	// The agent container gets NO GitHub credential in any form: no token
	// env, no literal token, and no reference to the per-Job secret.
	for _, env := range agent.Env {
		if env.Name == "GITHUB_TOKEN" {
			t.Error("agent container has a GITHUB_TOKEN env")
		}
		if strings.Contains(env.Value, spec.Token) {
			t.Errorf("agent env %s carries the token", env.Name)
		}
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name == name {
			t.Errorf("agent env %s references the per-Job secret", env.Name)
		}
	}
	for _, mount := range agent.VolumeMounts {
		if mount.Name == "input" {
			t.Error("agent container mounts the per-Job secret volume")
		}
	}
}

func TestCreateAgentEnv(t *testing.T) {
	cs := fake.NewClientset()
	c := New(cs, testConfig(), nil)
	spec := testSpec()

	name, err := c.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	job, err := cs.BatchV1().Jobs("patchy-agents").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	agent := job.Spec.Template.Spec.Containers[0]
	if got := strings.Join(agent.Command, " "); got != "agent-runner" {
		t.Errorf("agent command = %q, want agent-runner", got)
	}

	envs := map[string]corev1.EnvVar{}
	for _, env := range agent.Env {
		envs[env.Name] = env
	}
	wantValues := map[string]string{
		"HOME":                    "/workspace",
		"PATCHY_WORKSPACE":        "/workspace",
		"PATCHY_REPO":             "octo/repo",
		"PATCHY_ISSUE":            "42",
		"PATCHY_PHASE":            "classify+remediate",
		"PATCHY_CLASSIFY_MODEL":   "claude-sonnet-5",
		"PATCHY_CLASSIFY_TIMEOUT": "15m",
	}
	for k, want := range wantValues {
		if got, ok := envs[k]; !ok || got.Value != want {
			t.Errorf("agent env %s = %+v, want value %q", k, got, want)
		}
	}
	anthropic, ok := envs["ANTHROPIC_API_KEY"]
	if !ok || anthropic.ValueFrom == nil || anthropic.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("ANTHROPIC_API_KEY = %+v, want a secretKeyRef", anthropic)
	}
	ref := anthropic.ValueFrom.SecretKeyRef
	if ref.Name != "anthropic" || ref.Key != "api-key" {
		t.Errorf("ANTHROPIC_API_KEY ref = %s/%s, want anthropic/api-key (defaulted)", ref.Name, ref.Key)
	}
	// The credential channels not selected stay reserved: the passthrough
	// CLAUDE_CODE_OAUTH_TOKEN in Config.Env must not reach the pod.
	if got, ok := envs["CLAUDE_CODE_OAUTH_TOKEN"]; ok {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %+v, want absent (reserved)", got)
	}
}

func TestCreateAgentEnvOAuthToken(t *testing.T) {
	cs := fake.NewClientset()
	cfg := testConfig()
	cfg.AnthropicSecretEnv = "CLAUDE_CODE_OAUTH_TOKEN"
	c := New(cs, cfg, nil)

	name, err := c.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	job, err := cs.BatchV1().Jobs("patchy-agents").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	envs := map[string]corev1.EnvVar{}
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		envs[env.Name] = env
	}
	oauth, ok := envs["CLAUDE_CODE_OAUTH_TOKEN"]
	if !ok || oauth.ValueFrom == nil || oauth.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("CLAUDE_CODE_OAUTH_TOKEN = %+v, want a secretKeyRef (not the reserved passthrough literal)", oauth)
	}
	ref := oauth.ValueFrom.SecretKeyRef
	if ref.Name != "anthropic" || ref.Key != "api-key" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN ref = %s/%s, want anthropic/api-key", ref.Name, ref.Key)
	}
	if got, ok := envs["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("ANTHROPIC_API_KEY = %+v, want absent when the OAuth env is selected", got)
	}
}

func TestCreateSecurityContexts(t *testing.T) {
	cs := fake.NewClientset()
	c := New(cs, testConfig(), nil)

	name, err := c.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	job, err := cs.BatchV1().Jobs("patchy-agents").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	containers := append(job.Spec.Template.Spec.InitContainers, job.Spec.Template.Spec.Containers...)
	for _, ct := range containers {
		sc := ct.SecurityContext
		if sc == nil {
			t.Errorf("%s: no securityContext", ct.Name)
			continue
		}
		if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
			t.Errorf("%s: runAsNonRoot not true", ct.Name)
		}
		if sc.RunAsUser == nil || *sc.RunAsUser != 65532 {
			t.Errorf("%s: runAsUser = %v, want 65532", ct.Name, sc.RunAsUser)
		}
		if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
			t.Errorf("%s: allowPrivilegeEscalation not false", ct.Name)
		}
		if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
			t.Errorf("%s: readOnlyRootFilesystem not true", ct.Name)
		}
		if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
			t.Errorf("%s: capabilities = %+v, want drop ALL", ct.Name, sc.Capabilities)
		}
		if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
			t.Errorf("%s: seccompProfile = %+v, want RuntimeDefault", ct.Name, sc.SeccompProfile)
		}
		if cpu := ct.Resources.Requests.Cpu().String(); cpu != "500m" {
			t.Errorf("%s: cpu request = %s, want 500m", ct.Name, cpu)
		}
		if mem := ct.Resources.Limits.Memory().String(); mem != "4Gi" {
			t.Errorf("%s: memory limit = %s, want 4Gi", ct.Name, mem)
		}
	}
}

func TestCreateSecret(t *testing.T) {
	tests := []struct {
		name               string
		classification     string
		wantClassification bool
	}{
		{"classify+remediate has no classification", "", false},
		{"remediate re-run carries classification", "# Classification\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := fake.NewClientset()
			c := New(cs, testConfig(), nil)
			spec := testSpec()
			spec.ClassificationMarkdown = tt.classification

			name, err := c.Create(context.Background(), spec)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			secret, err := cs.CoreV1().Secrets("patchy-agents").Get(context.Background(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get secret: %v", err)
			}
			if got := string(secret.Data["token"]); got != spec.Token {
				t.Errorf("secret token = %q, want %q", got, spec.Token)
			}
			if got := string(secret.Data["issue.md"]); got != spec.IssueMarkdown {
				t.Errorf("secret issue.md = %q, want %q", got, spec.IssueMarkdown)
			}
			if _, ok := secret.Data["classification.md"]; ok != tt.wantClassification {
				t.Errorf("classification.md present = %v, want %v", ok, tt.wantClassification)
			}
			if len(secret.OwnerReferences) != 1 {
				t.Fatalf("secret ownerReferences = %+v, want exactly one", secret.OwnerReferences)
			}
			owner := secret.OwnerReferences[0]
			if owner.Kind != "Job" || owner.APIVersion != "batch/v1" || owner.Name != name {
				t.Errorf("secret owner = %+v, want batch/v1 Job %s", owner, name)
			}

			// The Job's volume must reference this secret so the init
			// container can stage the handoff files.
			job, err := cs.BatchV1().Jobs("patchy-agents").Get(context.Background(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get job: %v", err)
			}
			var found bool
			for _, vol := range job.Spec.Template.Spec.Volumes {
				if vol.Secret != nil && vol.Secret.SecretName == name {
					found = true
				}
			}
			if !found {
				t.Error("no pod volume references the per-Job secret")
			}
		})
	}
}

func TestCreateInvalidResources(t *testing.T) {
	cfg := testConfig()
	cfg.CPULimit = "not-a-quantity"
	c := New(fake.NewClientset(), cfg, nil)
	if _, err := c.Create(context.Background(), testSpec()); err == nil {
		t.Fatal("Create with invalid resource quantity succeeded, want error")
	}
}

func TestStatus(t *testing.T) {
	base := metav1.ObjectMeta{Name: "j", Namespace: "patchy-agents"}
	tests := []struct {
		name string
		job  batchv1.Job
		want Status
	}{
		{
			"active",
			batchv1.Job{ObjectMeta: base, Status: batchv1.JobStatus{Active: 1}},
			Status{Active: 1},
		},
		{
			"complete",
			batchv1.Job{ObjectMeta: base, Status: batchv1.JobStatus{
				Succeeded: 1,
				Conditions: []batchv1.JobCondition{
					{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
				},
			}},
			Status{Succeeded: 1, Done: true},
		},
		{
			"failed",
			batchv1.Job{ObjectMeta: base, Status: batchv1.JobStatus{
				Failed: 1,
				Conditions: []batchv1.JobCondition{
					{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
				},
			}},
			Status{Failed: 1, Done: true},
		},
		{
			"false condition is not done",
			batchv1.Job{ObjectMeta: base, Status: batchv1.JobStatus{
				Active: 1,
				Conditions: []batchv1.JobCondition{
					{Type: batchv1.JobComplete, Status: corev1.ConditionFalse},
				},
			}},
			Status{Active: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := tt.job
			c := New(fake.NewClientset(&job), testConfig(), nil)
			got, err := c.Status(context.Background(), "j")
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if got != tt.want {
				t.Errorf("Status = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStatusNotFound(t *testing.T) {
	c := New(fake.NewClientset(), testConfig(), nil)
	if _, err := c.Status(context.Background(), "missing"); err == nil {
		t.Fatal("Status of a missing job succeeded, want error")
	}
}

func TestDelete(t *testing.T) {
	cs := fake.NewClientset()
	c := New(cs, testConfig(), nil)
	name, err := c.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := c.Delete(context.Background(), name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = cs.BatchV1().Jobs("patchy-agents").Get(context.Background(), name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("job still present after Delete: %v", err)
	}
	if err := c.Delete(context.Background(), name); err == nil {
		t.Error("Delete of a missing job succeeded, want error")
	}
}

func TestList(t *testing.T) {
	cs := fake.NewClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "patchy-agents"},
	})
	c := New(cs, testConfig(), nil)

	specs := []Spec{testSpec(), testSpec()}
	specs[1].Repo = "octo/other"
	specs[1].Issue = 7
	specs[1].Attempt = 3
	for _, spec := range specs {
		if _, err := c.Create(context.Background(), spec); err != nil {
			t.Fatalf("Create(%+v): %v", spec, err)
		}
	}

	owned, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(owned) != 2 {
		t.Fatalf("List returned %d jobs, want 2 (unlabelled jobs excluded)", len(owned))
	}
	byName := map[string]Owned{}
	for _, o := range owned {
		byName[o.Name] = o
	}
	for _, spec := range specs {
		o, ok := byName[Name(spec.Repo, spec.Issue, spec.Attempt)]
		if !ok {
			t.Errorf("List is missing job for %s#%d", spec.Repo, spec.Issue)
			continue
		}
		if o.Repo != spec.Repo || o.Issue != spec.Issue || o.Attempt != spec.Attempt {
			t.Errorf("List round-trip = %+v, want repo %s issue %d attempt %d",
				o, spec.Repo, spec.Issue, spec.Attempt)
		}
	}
}

func TestSanitizeLabelValue(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"octo/repo", "octo-repo"},
		{"Octo/RePo", "octo-repo"},
		{"a/b.c_d-e", "a-b.c_d-e"},
		{"---", "unknown"},
		{strings.Repeat("x", 70) + "/y", strings.Repeat("x", 63)},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := sanitizeLabelValue(tt.in); got != tt.want {
				t.Errorf("sanitizeLabelValue(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
