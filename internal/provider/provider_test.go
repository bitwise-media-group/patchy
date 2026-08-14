// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package provider

import (
	"maps"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/model"
)

const broker = "http://patchy-egress-broker.patchy.svc.cluster.local:8080"

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"anthropic ok", Config{Name: Anthropic, BrokerURL: broker}, ""},
		{"unknown provider", Config{Name: "openai", BrokerURL: broker}, "not one of"},
		{"no broker url", Config{Name: Anthropic}, "broker URL is required"},
		{"bedrock needs region", Config{Name: Bedrock, BrokerURL: broker}, "region is required"},
		{"bedrock ok", Config{Name: Bedrock, BrokerURL: broker, Region: "us-east-1"}, ""},
		{"vertex needs project", Config{Name: Vertex, BrokerURL: broker, Region: "europe-west1"},
			"region and project id"},
		{"vertex ok", Config{Name: Vertex, BrokerURL: broker, Region: "europe-west1", ProjectID: "p"}, ""},
		{"foundry ok", Config{Name: Foundry, BrokerURL: broker}, ""},
		{"credential extra env", Config{Name: Anthropic, BrokerURL: broker,
			ExtraEnv: map[string]string{"ANTHROPIC_API_KEY": "sk"}}, "cannot be set here"},
		{"gateway extra env", Config{Name: Anthropic, BrokerURL: broker,
			ExtraEnv: map[string]string{"ANTHROPIC_BASE_URL": "https://elsewhere"}}, "cannot be set here"},
		{"benign extra env", Config{Name: Anthropic, BrokerURL: broker,
			ExtraEnv: map[string]string{"HTTPS_PROXY": "http://proxy"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestEnv(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want map[string]string
	}{
		{
			"anthropic",
			Config{Name: Anthropic, BrokerURL: broker + "/"},
			map[string]string{
				"ANTHROPIC_BASE_URL":                       broker + "/anthropic",
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			},
		},
		{
			"bedrock",
			Config{Name: Bedrock, BrokerURL: broker, Region: "us-east-1"},
			map[string]string{
				"CLAUDE_CODE_USE_BEDROCK":                  "1",
				"CLAUDE_CODE_SKIP_BEDROCK_AUTH":            "1",
				"ANTHROPIC_BEDROCK_BASE_URL":               broker + "/bedrock",
				"AWS_REGION":                               "us-east-1",
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			},
		},
		{
			"vertex",
			Config{Name: Vertex, BrokerURL: broker, Region: "europe-west1", ProjectID: "proj"},
			map[string]string{
				"CLAUDE_CODE_USE_VERTEX":                   "1",
				"CLAUDE_CODE_SKIP_VERTEX_AUTH":             "1",
				"ANTHROPIC_VERTEX_BASE_URL":                broker + "/vertex",
				"ANTHROPIC_VERTEX_PROJECT_ID":              "proj",
				"CLOUD_ML_REGION":                          "europe-west1",
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			},
		},
		{
			"foundry",
			Config{Name: Foundry, BrokerURL: broker},
			map[string]string{
				"CLAUDE_CODE_USE_FOUNDRY":                  "1",
				"CLAUDE_CODE_SKIP_FOUNDRY_AUTH":            "1",
				"ANTHROPIC_FOUNDRY_BASE_URL":               broker + "/foundry",
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Env(tt.cfg, nil)
			if !maps.Equal(got, tt.want) {
				t.Fatalf("Env() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvModelMapAndExtra(t *testing.T) {
	cfg := Config{Name: Anthropic, BrokerURL: broker, ExtraEnv: map[string]string{"HTTPS_PROXY": "http://p"}}
	got := Env(cfg, map[string]string{"anthropic/claude-sonnet-5": "claude-sonnet-5"})
	if got["PATCHY_MODEL_MAP"] != "anthropic/claude-sonnet-5=claude-sonnet-5" {
		t.Errorf("PATCHY_MODEL_MAP = %q", got["PATCHY_MODEL_MAP"])
	}
	if got["HTTPS_PROXY"] != "http://p" {
		t.Errorf("ExtraEnv not passed through: %v", got)
	}
}

func TestEffectiveModelMap(t *testing.T) {
	models := model.Builtins()

	t.Run("bedrock derives prefixed ids", func(t *testing.T) {
		got, err := EffectiveModelMap(Config{Name: Bedrock, BrokerURL: broker, Region: "us-east-1"}, models)
		if err != nil {
			t.Fatal(err)
		}
		if id := got["anthropic/claude-sonnet-5"]; id != "us.anthropic.claude-sonnet-5" {
			t.Errorf("sonnet id = %q", id)
		}
		if _, ok := got["openai/gpt-5.3-codex"]; ok {
			t.Error("non-claude model mapped")
		}
	})
	t.Run("bedrock eu region", func(t *testing.T) {
		got, err := EffectiveModelMap(Config{Name: Bedrock, BrokerURL: broker, Region: "eu-west-2"}, models)
		if err != nil {
			t.Fatal(err)
		}
		if id := got["anthropic/claude-haiku-4-5"]; id != "eu.anthropic.claude-haiku-4-5" {
			t.Errorf("haiku id = %q", id)
		}
	})
	t.Run("bedrock ap region derives apac", func(t *testing.T) {
		got, err := EffectiveModelMap(Config{Name: Bedrock, BrokerURL: broker, Region: "ap-southeast-2"}, models)
		if err != nil {
			t.Fatal(err)
		}
		if id := got["anthropic/claude-sonnet-5"]; id != "apac.anthropic.claude-sonnet-5" {
			t.Errorf("sonnet id = %q", id)
		}
	})
	t.Run("bedrock underivable region errors", func(t *testing.T) {
		_, err := EffectiveModelMap(Config{Name: Bedrock, BrokerURL: broker, Region: "ca-central-1"}, models)
		if err == nil || !strings.Contains(err.Error(), "region prefix") {
			t.Fatalf("err = %v, want region-prefix error", err)
		}
	})
	t.Run("bedrock explicit prefix wins", func(t *testing.T) {
		got, err := EffectiveModelMap(Config{
			Name: Bedrock, BrokerURL: broker, Region: "ca-central-1", RegionPrefix: "us",
		}, models)
		if err != nil {
			t.Fatal(err)
		}
		if id := got["anthropic/claude-sonnet-5"]; id != "us.anthropic.claude-sonnet-5" {
			t.Errorf("sonnet id = %q", id)
		}
	})
	t.Run("vertex derives bare ids", func(t *testing.T) {
		got, err := EffectiveModelMap(Config{
			Name: Vertex, BrokerURL: broker, Region: "europe-west1", ProjectID: "p",
		}, models)
		if err != nil {
			t.Fatal(err)
		}
		if id := got["anthropic/claude-opus-5"]; id != "claude-opus-5" {
			t.Errorf("opus id = %q", id)
		}
	})
	t.Run("operator override wins", func(t *testing.T) {
		got, err := EffectiveModelMap(Config{
			Name: Bedrock, BrokerURL: broker, Region: "us-east-1",
			ModelMap: map[string]string{"anthropic/claude-sonnet-5": "us.anthropic.claude-sonnet-5-v2:0"},
		}, models)
		if err != nil {
			t.Fatal(err)
		}
		if id := got["anthropic/claude-sonnet-5"]; id != "us.anthropic.claude-sonnet-5-v2:0" {
			t.Errorf("sonnet id = %q", id)
		}
	})
	t.Run("foundry is the operator map exactly", func(t *testing.T) {
		mm := map[string]string{"anthropic/claude-sonnet-5": "my-sonnet-deployment"}
		got, err := EffectiveModelMap(Config{Name: Foundry, BrokerURL: broker, ModelMap: mm}, models)
		if err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(got, mm) {
			t.Errorf("map = %v, want %v", got, mm)
		}
	})
}

func TestValidateCoverage(t *testing.T) {
	models := model.Builtins()
	cfg := Config{Name: Foundry, BrokerURL: broker}
	mm := map[string]string{"anthropic/claude-sonnet-5": "sonnet-deploy"}

	if err := ValidateCoverage(cfg, models, mm, []string{"anthropic/claude-sonnet-5"}); err != nil {
		t.Errorf("covered model rejected: %v", err)
	}
	err := ValidateCoverage(cfg, models, mm, []string{"anthropic/claude-sonnet-5", "anthropic/claude-opus-5"})
	if err == nil || !strings.Contains(err.Error(), "anthropic/claude-opus-5") {
		t.Errorf("uncovered model accepted: %v", err)
	}
	// A model another harness runs is not this runner's problem.
	if err := ValidateCoverage(cfg, models, mm, []string{"openai/gpt-5.3-codex"}); err != nil {
		t.Errorf("codex model demanded a claude map entry: %v", err)
	}
	// Anthropic never needs a map.
	if err := ValidateCoverage(Config{Name: Anthropic, BrokerURL: broker}, models, nil,
		[]string{"anthropic/claude-sonnet-5"}); err != nil {
		t.Errorf("anthropic coverage failed: %v", err)
	}
}

func TestModelMapCodec(t *testing.T) {
	in := map[string]string{
		"anthropic/claude-sonnet-5": "us.anthropic.claude-sonnet-5",
		"anthropic/claude-opus-5":   "us.anthropic.claude-opus-5",
	}
	wire := FormatModelMap(in)
	want := "anthropic/claude-opus-5=us.anthropic.claude-opus-5," +
		"anthropic/claude-sonnet-5=us.anthropic.claude-sonnet-5"
	if wire != want {
		t.Errorf("FormatModelMap = %q, want %q (sorted)", wire, want)
	}
	out, err := ParseModelMap(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(in, out) {
		t.Errorf("round trip = %v, want %v", out, in)
	}

	if m, err := ParseModelMap("  "); err != nil || m != nil {
		t.Errorf("blank map = %v, %v; want nil, nil", m, err)
	}
	if _, err := ParseModelMap("no-equals"); err == nil {
		t.Error("malformed entry accepted")
	}
	if _, err := ParseModelMap("=x"); err == nil {
		t.Error("empty canonical id accepted")
	}
}
