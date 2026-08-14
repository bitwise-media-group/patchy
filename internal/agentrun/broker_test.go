// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentrun

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/harness"
)

func TestCliModel(t *testing.T) {
	tests := []struct {
		name      string
		canonical string
		harnessID string
		modelMap  map[string]string
		want      string
	}{
		{"registry id without a map", "anthropic/claude-sonnet-5", "claude", nil, "claude-sonnet-5"},
		{"model map wins over the registry", "anthropic/claude-sonnet-5", "claude",
			map[string]string{"anthropic/claude-sonnet-5": "us.anthropic.claude-sonnet-5"},
			"us.anthropic.claude-sonnet-5"},
		{"unmapped model falls back to the registry", "anthropic/claude-opus-5", "claude",
			map[string]string{"anthropic/claude-sonnet-5": "us.anthropic.claude-sonnet-5"},
			"claude-opus-5"},
		{"unknown id falls back to the bare id", "anthropic/claude-next-99", "claude", nil, "claude-next-99"},
		{"unqualified unknown id passes through", "mystery", "claude", nil, "mystery"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{cfg: Config{ModelMap: tt.modelMap}}
			if got := a.cliModel(tt.canonical, tt.harnessID); got != tt.want {
				t.Errorf("cliModel(%q, %q) = %q, want %q", tt.canonical, tt.harnessID, got, tt.want)
			}
		})
	}
}

func TestBrokerTokenInjectedPerStage(t *testing.T) {
	ws := newWorkspace(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("stale-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cfg := newConfig(t, ws, &out)
	cfg.BrokerTokenFile = tokenFile
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, writes: map[string]string{
			"reports/investigation.md": goodInvestigation,
		}},
	}}
	agent := New(cfg, fx)

	// The kubelet rotates the projection; the stage must read the file at
	// run time, not at construction.
	if err := os.WriteFile(tokenFile, []byte("fresh-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(fx.specs) != 1 {
		t.Fatalf("stages run = %d, want 1", len(fx.specs))
	}
	want := "ANTHROPIC_CUSTOM_HEADERS=X-Patchy-Broker-Token: fresh-token"
	if !slices.Contains(fx.specs[0].Env, want) {
		t.Errorf("stage env = %v, want it to contain %q", fx.specs[0].Env, want)
	}
	// The raw token is registered for transcript scrubbing.
	if !slices.Contains(agent.scrub, "fresh-token") {
		t.Errorf("scrub values = %v, want the raw token registered", agent.scrub)
	}
}

func TestBrokerTokenMissingFailsTheStage(t *testing.T) {
	ws := newWorkspace(t)
	var out bytes.Buffer
	cfg := newConfig(t, ws, &out)
	cfg.BrokerTokenFile = filepath.Join(t.TempDir(), "absent")
	fx := &fakeExec{}

	if err := New(cfg, fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v (stage outcomes are events, not errors)", err)
	}
	evs := events(t, out.String())
	if len(evs) != 1 || evs[0].Investigation == nil {
		t.Fatalf("events = %+v, want one investigation event", evs)
	}
	if evs[0].Investigation.Outcome != envelope.OutcomeRuntimeError {
		t.Errorf("outcome = %q, want runtime error for an unreadable token", evs[0].Investigation.Outcome)
	}
	if len(fx.specs) != 0 {
		t.Error("stage ran despite the unreadable broker token")
	}
}

func TestNoBrokerEnvWhenNotBrokered(t *testing.T) {
	ev := investigateRun(t) // newConfig sets no BrokerTokenFile
	if ev.Investigation.Outcome != envelope.OutcomeOK {
		t.Fatalf("outcome = %q", ev.Investigation.Outcome)
	}
}

func TestCredentialValuesIncludesRegistered(t *testing.T) {
	h, _ := harness.ByID("claude")
	got := credentialValues(h, "registered-token", "")
	if !slices.Contains(got, "registered-token") {
		t.Errorf("credentialValues = %v, want the registered value", got)
	}
	if slices.Contains(got, "") {
		t.Error("empty registered value kept")
	}
}

func TestFromEnvBrokerFields(t *testing.T) {
	env := map[string]string{
		"PATCHY_REPO":              "acme/shop",
		"PATCHY_FINDING":           "finding-1",
		"PATCHY_BROKER_TOKEN_FILE": "/var/run/patchy/broker/token",
		"PATCHY_MODEL_MAP":         "anthropic/claude-sonnet-5=us.anthropic.claude-sonnet-5",
	}
	cfg, err := FromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.BrokerTokenFile != "/var/run/patchy/broker/token" {
		t.Errorf("BrokerTokenFile = %q", cfg.BrokerTokenFile)
	}
	if cfg.ModelMap["anthropic/claude-sonnet-5"] != "us.anthropic.claude-sonnet-5" {
		t.Errorf("ModelMap = %v", cfg.ModelMap)
	}

	env["PATCHY_MODEL_MAP"] = "garbage"
	if _, err := FromEnv(func(k string) string { return env[k] }); err == nil ||
		!strings.Contains(err.Error(), "PATCHY_MODEL_MAP") {
		t.Errorf("FromEnv() error = %v, want a PATCHY_MODEL_MAP rejection", err)
	}
}
