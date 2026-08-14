// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/bitwise-media-group/patchy/internal/provider"
)

// brokeredConfig is testConfig with the claude runner in brokered mode: no
// Secret, gateway env instead.
func brokeredConfig() Config {
	cfg := testConfig()
	cfg.Runners["claude"] = Runner{
		Image:    "ghcr.io/bitwise-media-group/patchy/claude-agent-runner:1",
		Brokered: true,
		Env: map[string]string{
			"ANTHROPIC_BASE_URL":                       "http://patchy-egress-broker.patchy.svc.cluster.local:8080/anthropic",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			// A credential channel must never ride the per-runner env, even
			// if a bug upstream put one there.
			"ANTHROPIC_API_KEY": "must-never-pass-through",
		},
	}
	return cfg
}

func createJob(t *testing.T, cfg Config, spec Spec) *batchv1.Job {
	t.Helper()
	cs := fake.NewClientset()
	c := New(cs, cfg, nil)
	name, err := c.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	job, err := cs.BatchV1().Jobs(cfg.Namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	return job
}

func envMap(ct corev1.Container) map[string]corev1.EnvVar {
	out := map[string]corev1.EnvVar{}
	for _, e := range ct.Env {
		out[e.Name] = e
	}
	return out
}

func brokerVolume(pod corev1.PodSpec) *corev1.ServiceAccountTokenProjection {
	for _, v := range pod.Volumes {
		if v.Name != volBrokerToken {
			continue
		}
		if v.Projected == nil || len(v.Projected.Sources) != 1 {
			return nil
		}
		return v.Projected.Sources[0].ServiceAccountToken
	}
	return nil
}

func TestBrokeredJobShape(t *testing.T) {
	job := createJob(t, brokeredConfig(), testSpec())
	pod := job.Spec.Template.Spec
	agent := pod.Containers[0]
	prepare := pod.InitContainers[0]

	proj := brokerVolume(pod)
	if proj == nil {
		t.Fatal("no projected broker-token volume on the pod")
	}
	if proj.Audience != DefaultBrokerAudience {
		t.Errorf("token audience = %q, want %q", proj.Audience, DefaultBrokerAudience)
	}
	if proj.Path != "token" {
		t.Errorf("token path = %q, want token", proj.Path)
	}
	// testConfig's deadline is 1h; the finding TTL adds 15m headroom.
	if want := int64((time.Hour + 15*time.Minute).Seconds()); proj.ExpirationSeconds == nil ||
		*proj.ExpirationSeconds != want {
		t.Errorf("token expiration = %v, want %d", proj.ExpirationSeconds, want)
	}

	var mounted bool
	for _, m := range agent.VolumeMounts {
		if m.Name == volBrokerToken {
			mounted = true
			if !m.ReadOnly || m.MountPath != brokerTokenDir {
				t.Errorf("broker token mount = %+v, want read-only at %s", m, brokerTokenDir)
			}
		}
	}
	if !mounted {
		t.Error("agent container does not mount the broker token")
	}
	for _, m := range prepare.VolumeMounts {
		if m.Name == volBrokerToken {
			t.Error("init container mounts the broker token; the prepare step must stay identity-free")
		}
	}

	envs := envMap(agent)
	if got := envs["PATCHY_BROKER_TOKEN_FILE"].Value; got != brokerTokenPath {
		t.Errorf("PATCHY_BROKER_TOKEN_FILE = %q, want %q", got, brokerTokenPath)
	}
	if got := envs["ANTHROPIC_BASE_URL"].Value; !strings.HasSuffix(got, "/anthropic") {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want the broker route", got)
	}
	// No model credential in any form: no SecretKeyRef, no credential value.
	for _, e := range agent.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			t.Errorf("brokered agent env %s references a Secret", e.Name)
		}
	}
	if _, ok := envs["ANTHROPIC_API_KEY"]; ok {
		t.Error("credential channel leaked through the per-runner env")
	}
}

func TestFindingTokenTTLFloor(t *testing.T) {
	if got := findingTokenTTL(0); got != 3600 {
		t.Errorf("findingTokenTTL(0) = %d, want the 3600 floor", got)
	}
	if got := findingTokenTTL(10 * time.Minute); got != 3600 {
		t.Errorf("findingTokenTTL(10m) = %d, want the 3600 floor", got)
	}
	if got := findingTokenTTL(2 * time.Hour); got != int64((2*time.Hour + 15*time.Minute).Seconds()) {
		t.Errorf("findingTokenTTL(2h) = %d", got)
	}
}

func TestReservedEnvCoversGatewayNames(t *testing.T) {
	for _, name := range provider.GatewayEnvNames {
		if !reservedEnv[name] {
			t.Errorf("gateway env %s is not reserved; Config.Env could shadow it", name)
		}
	}
}

func TestRunnerEnvWinsOverConfigEnv(t *testing.T) {
	cfg := brokeredConfig()
	cfg.Env["SHARED_KNOB"] = "global"
	claude := cfg.Runners["claude"]
	claude.Env["SHARED_KNOB"] = "runner"
	cfg.Runners["claude"] = claude

	job := createJob(t, cfg, testSpec())
	envs := envMap(job.Spec.Template.Spec.Containers[0])
	if got := envs["SHARED_KNOB"].Value; got != "runner" {
		t.Errorf("SHARED_KNOB = %q, want the per-runner value to win", got)
	}
}

func TestNonBrokeredJobUnchanged(t *testing.T) {
	// codex keeps its Secret channel and gains nothing broker-shaped.
	spec := testSpec()
	spec.Harness = "codex"
	job := createJob(t, brokeredConfig(), spec)
	pod := job.Spec.Template.Spec

	if brokerVolume(pod) != nil {
		t.Error("non-brokered pod carries a broker-token volume")
	}
	agent := pod.Containers[0]
	envs := envMap(agent)
	if _, ok := envs["PATCHY_BROKER_TOKEN_FILE"]; ok {
		t.Error("non-brokered agent got PATCHY_BROKER_TOKEN_FILE")
	}
	sec := envs["OPENAI_API_KEY"]
	if sec.ValueFrom == nil || sec.ValueFrom.SecretKeyRef == nil {
		t.Error("codex lost its Secret credential channel")
	}
	for _, m := range agent.VolumeMounts {
		if m.Name == volBrokerToken {
			t.Error("non-brokered agent mounts the broker token")
		}
	}
}

func TestBrokeredEvalJobShape(t *testing.T) {
	cfg := brokeredConfig()
	cs := fake.NewClientset()
	c := New(cs, cfg, nil)
	name, err := c.CreateEval(context.Background(), EvalSpec{
		Evaluation: "eval-1", Unit: "eval-1-u0", Index: 0, Harness: "claude",
		UnitJSON:    []byte(`{}`),
		ArtifactURL: "http://blobs/x", ArtifactDigest: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("CreateEval: %v", err)
	}
	job, err := cs.BatchV1().Jobs(cfg.Namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	pod := job.Spec.Template.Spec
	agent := pod.Containers[0]

	proj := brokerVolume(pod)
	if proj == nil {
		t.Fatal("no projected broker-token volume on the eval pod")
	}
	// The eval pod captures the token once at start, so the TTL covers the
	// whole deadline plus an hour.
	if want := int64((time.Hour + time.Hour).Seconds()); proj.ExpirationSeconds == nil ||
		*proj.ExpirationSeconds != want {
		t.Errorf("token expiration = %v, want %d", proj.ExpirationSeconds, want)
	}

	cmd := strings.Join(agent.Command, " ")
	if !strings.Contains(cmd, "exec evolve exec-unit") {
		t.Errorf("eval command = %q, want the exec wrapper", cmd)
	}
	if !strings.Contains(cmd, "ANTHROPIC_CUSTOM_HEADERS=") ||
		!strings.Contains(cmd, provider.BrokerTokenHeader) {
		t.Errorf("eval command = %q, want the caller-token export", cmd)
	}
	envs := envMap(agent)
	if got := envs["ANTHROPIC_BASE_URL"].Value; !strings.HasSuffix(got, "/anthropic") {
		t.Errorf("eval ANTHROPIC_BASE_URL = %q, want the broker route", got)
	}
	for _, e := range agent.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			t.Errorf("brokered eval env %s references a Secret", e.Name)
		}
	}
	if _, ok := envs["ANTHROPIC_API_KEY"]; ok {
		t.Error("credential channel leaked into the eval env")
	}
}

func TestBrokeredEvalDeadlinePastTokenCap(t *testing.T) {
	cfg := brokeredConfig()
	c := New(fake.NewClientset(), cfg, nil)
	_, err := c.CreateEval(context.Background(), EvalSpec{
		Evaluation: "eval-1", Unit: "eval-1-u0", Harness: "claude",
		UnitJSON: []byte(`{}`), ArtifactURL: "http://blobs/x", ArtifactDigest: strings.Repeat("a", 64),
		Deadline: 23*time.Hour + 30*time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "24h") {
		t.Fatalf("CreateEval error = %v, want the 24h projection-cap rejection", err)
	}
}
