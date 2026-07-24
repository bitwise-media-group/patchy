// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func secret(name string, keys ...string) *corev1.Secret {
	data := map[string][]byte{}
	for _, k := range keys {
		data[k] = []byte("x")
	}
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy-agents"}, Data: data}
}

func fleet() map[string]Runner {
	return map[string]Runner{
		"claude": {Image: "claude:1", Secret: "anthropic", SecretKey: "api-key", SecretEnv: "ANTHROPIC_API_KEY"},
		"codex":  {Image: "codex:1", Secret: "openai", SecretKey: "api-key", SecretEnv: "OPENAI_API_KEY"},
	}
}

func TestResolveRunners(t *testing.T) {
	ctx := context.Background()

	// Auto-detect: only harnesses whose Secret exists (with the key) are enabled.
	cs := fake.NewClientset(secret("anthropic", "api-key"))
	enabled, err := ResolveRunners(ctx, cs, "patchy-agents", fleet(), nil)
	if err != nil {
		t.Fatalf("auto-detect: %v", err)
	}
	if !slices.Equal(enabled, []string{"claude"}) {
		t.Errorf("auto-detect enabled = %v, want [claude]", enabled)
	}

	// Both credentials present → both enabled.
	cs = fake.NewClientset(secret("anthropic", "api-key"), secret("openai", "api-key"))
	enabled, err = ResolveRunners(ctx, cs, "patchy-agents", fleet(), nil)
	if err != nil || !slices.Equal(enabled, []string{"claude", "codex"}) {
		t.Errorf("both present: enabled = %v (%v), want [claude codex]", enabled, err)
	}

	// Restrict names codex but its Secret is missing → error naming codex.
	cs = fake.NewClientset(secret("anthropic", "api-key"))
	_, err = ResolveRunners(ctx, cs, "patchy-agents", fleet(), []string{"codex"})
	if err == nil || !strings.Contains(err.Error(), "codex") {
		t.Errorf("restrict-missing: err = %v, want it to name codex", err)
	}

	// A Secret present but missing the configured key is a hard error.
	cs = fake.NewClientset(secret("anthropic", "wrong-key"))
	_, err = ResolveRunners(ctx, cs, "patchy-agents", fleet(), nil)
	if err == nil || !strings.Contains(err.Error(), "no key") {
		t.Errorf("missing-key: err = %v, want a missing-key error", err)
	}
}

func TestResolveRunnersFakeAlwaysEnabled(t *testing.T) {
	runners := map[string]Runner{"fake": {Image: "fake:1"}}
	enabled, err := ResolveRunners(context.Background(), fake.NewClientset(), "patchy-agents", runners, nil)
	if err != nil || !slices.Equal(enabled, []string{"fake"}) {
		t.Errorf("fake enabled = %v (%v), want [fake] (no credential needed)", enabled, err)
	}
}
