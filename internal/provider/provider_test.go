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

// TestEffectiveModelMapSynthesized: a pattern-tier model passed in through
// model.WithResolved gets derived Bedrock and Vertex ids like a registered one.
func TestEffectiveModelMapSynthesized(t *testing.T) {
	models := model.Builtins()
	withNew := model.WithResolved(models, "anthropic/claude-opus-6")
	bedrock, err := EffectiveModelMap(Config{Name: Bedrock, BrokerURL: broker, Region: "us-east-1"}, withNew)
	if err != nil {
		t.Fatal(err)
	}
	if id := bedrock["anthropic/claude-opus-6"]; id != "us.anthropic.claude-opus-6" {
		t.Errorf("bedrock synthesized id = %q", id)
	}
	vertex, err := EffectiveModelMap(Config{
		Name: Vertex, BrokerURL: broker, Region: "global", ProjectID: "p",
	}, withNew)
	if err != nil {
		t.Fatal(err)
	}
	if id := vertex["anthropic/claude-opus-6"]; id != "claude-opus-6" {
		t.Errorf("vertex synthesized id = %q", id)
	}
	// Not passed in: the registry alone knows nothing of it.
	plain, _ := EffectiveModelMap(Config{Name: Bedrock, BrokerURL: broker, Region: "us-east-1"}, models)
	if _, ok := plain["anthropic/claude-opus-6"]; ok {
		t.Error("unregistered model mapped without being passed in")
	}
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
	// An alias is checked under the concrete id it resolves to — a Foundry
	// map keyed by the alias does not cover it.
	err = ValidateCoverage(cfg, models, map[string]string{"anthropic/claude-sonnet": "x"},
		[]string{"anthropic/claude-sonnet"})
	if err == nil || !strings.Contains(err.Error(), "anthropic/claude-sonnet-5-5") {
		t.Errorf("alias coverage error = %v, want the concrete id named", err)
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

func TestValidateCodex(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"openai ok", Config{Harness: model.HarnessCodex, Name: OpenAI, BrokerURL: broker}, ""},
		{"azure ok", Config{Harness: model.HarnessCodex, Name: Azure, BrokerURL: broker}, ""},
		{"claude provider", Config{Harness: model.HarnessCodex, Name: Anthropic, BrokerURL: broker},
			"codex provider"},
		{"no broker url", Config{Harness: model.HarnessCodex, Name: OpenAI}, "codex runs proxy-only"},
		{"control character in url", Config{Harness: model.HarnessCodex, Name: OpenAI,
			BrokerURL: broker + "\n[x]"}, "control character"},
		{"codex credential extra env", Config{Harness: model.HarnessCodex, Name: OpenAI, BrokerURL: broker,
			ExtraEnv: map[string]string{"CODEX_API_KEY": "sk"}}, "cannot be set here"},
		{"codex config extra env", Config{Harness: model.HarnessCodex, Name: OpenAI, BrokerURL: broker,
			ExtraEnv: map[string]string{CodexConfigEnv: "x"}}, "cannot be set here"},
		{"fake cannot be brokered", Config{Harness: model.HarnessFake, Name: OpenAI, BrokerURL: broker},
			"cannot be brokered"},
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

// TestCodexEnv: a codex runner's gateway env is the rendered config.toml plus
// the model map and passthrough — none of claude's env.
func TestCodexEnv(t *testing.T) {
	cfg := Config{Harness: model.HarnessCodex, Name: Azure, BrokerURL: broker + "/",
		ExtraEnv: map[string]string{"HTTPS_PROXY": "http://proxy"}}
	env := Env(cfg, map[string]string{"openai/gpt-5.3-codex": "codex-deploy"})
	want := map[string]string{
		CodexConfigEnv:     CodexConfig(cfg),
		"PATCHY_MODEL_MAP": "openai/gpt-5.3-codex=codex-deploy",
		"HTTPS_PROXY":      "http://proxy",
	}
	if !maps.Equal(env, want) {
		t.Errorf("Env() = %v, want %v", env, want)
	}
}

func TestCodexConfig(t *testing.T) {
	got := CodexConfig(Config{Harness: model.HarnessCodex, Name: OpenAI, BrokerURL: broker + "/"})
	for _, want := range []string{
		`model_provider = "patchy-broker"`,
		"check_for_update_on_startup = false",
		"[analytics]\nenabled = false",
		"[model_providers.patchy-broker]",
		`base_url = "` + broker + `/openai/v1"`,
		`wire_api = "responses"`,
		`env_key = "` + CodexPlaceholderKeyEnv + `"`,
		`env_http_headers = { "` + BrokerTokenHeader + `" = "` + BrokerTokenEnv + `" }`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CodexConfig() missing %q:\n%s", want, got)
		}
	}
	if got := tomlString(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("tomlString = %s", got)
	}
}

// TestCodexEffectiveModelMapAndCoverage: openai needs no map (registry ids
// are codex's ids); azure is operator-written and must cover every codex
// model it will run, ignoring claude's.
func TestCodexEffectiveModelMapAndCoverage(t *testing.T) {
	models := model.Builtins()
	required := []string{"openai/gpt-5.3-codex", "openai/gpt-5.6-sol", "anthropic/claude-sonnet-5-5"}

	openai := Config{Harness: model.HarnessCodex, Name: OpenAI, BrokerURL: broker}
	mm, err := EffectiveModelMap(openai, models)
	if err != nil || len(mm) != 0 {
		t.Fatalf("openai EffectiveModelMap = %v, %v; want empty", mm, err)
	}
	if err := ValidateCoverage(openai, models, mm, required); err != nil {
		t.Errorf("openai coverage: %v", err)
	}

	azure := Config{Harness: model.HarnessCodex, Name: Azure, BrokerURL: broker,
		ModelMap: map[string]string{"openai/gpt-5.3-codex": "codex-deploy"}}
	mm, err = EffectiveModelMap(azure, models)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateCoverage(azure, models, mm, required)
	if err == nil || !strings.Contains(err.Error(), "openai/gpt-5.6-sol") ||
		strings.Contains(err.Error(), "claude") {
		t.Errorf("azure coverage error = %v, want only the uncovered codex model", err)
	}
}
