// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package provider

import (
	"fmt"
	"strings"
)

// The codex CLI has no env-only channel for a custom request header the way
// claude has ANTHROPIC_CUSTOM_HEADERS, and its built-in openai provider
// cannot be extended. A brokered codex run therefore selects a custom model
// provider declared in config.toml — written in-pod from CodexConfigEnv to
// ${CODEX_HOME:-$HOME/.codex}/config.toml — whose base_url is the broker's
// route and whose headers are read from the child process's env at request
// time:
//
//   - BrokerTokenEnv carries the projected caller token, sent as
//     BrokerTokenHeader (env_http_headers). Set per stage, never on the pod.
//   - CodexPlaceholderKeyEnv carries BrokerAuthPlaceholder as the provider's
//     env_key, so codex has an "API key" and sends Authorization: Bearer
//     patchy-egress-broker. The broker strips it before injecting the real
//     credential; like claude's placeholder it grants nothing.
const (
	CodexConfigEnv         = "PATCHY_CODEX_CONFIG"
	BrokerTokenEnv         = "PATCHY_BROKER_TOKEN"
	CodexPlaceholderKeyEnv = "PATCHY_BROKER_API_KEY"
)

// codexProviderID is the model_providers table the rendered config selects.
// Built-in ids (openai, ollama, ...) are reserved and cannot be overridden.
const codexProviderID = "patchy-broker"

// CodexConfig renders the config.toml a brokered codex run uses: the custom
// broker provider (Responses API at <broker>/<route>/v1), with the update
// check, analytics and feedback uploads off — the broker is the pod's only
// egress and proxies model traffic alone, the codex analogue of claude's
// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC.
func CodexConfig(c Config) string {
	base := strings.TrimRight(c.BrokerURL, "/") + "/" + c.Name + "/v1"
	var b strings.Builder
	b.WriteString("# Rendered by patchy: all model traffic goes through the egress broker.\n")
	fmt.Fprintf(&b, "model_provider = %s\n", tomlString(codexProviderID))
	b.WriteString("check_for_update_on_startup = false\n\n")
	b.WriteString("[analytics]\nenabled = false\n\n")
	b.WriteString("[feedback]\nenabled = false\n\n")
	fmt.Fprintf(&b, "[model_providers.%s]\n", codexProviderID)
	b.WriteString("name = \"patchy egress broker\"\n")
	fmt.Fprintf(&b, "base_url = %s\n", tomlString(base))
	b.WriteString("wire_api = \"responses\"\n")
	fmt.Fprintf(&b, "env_key = %s\n", tomlString(CodexPlaceholderKeyEnv))
	fmt.Fprintf(&b, "env_http_headers = { %s = %s }\n", tomlString(BrokerTokenHeader), tomlString(BrokerTokenEnv))
	return b.String()
}

// tomlString renders s as a TOML basic string. Validate has already rejected
// control characters (checkTOMLSafe), so only the quote and backslash need
// escaping.
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// checkTOMLSafe rejects values that cannot be embedded in a single-line TOML
// basic string without escape sequences tomlString does not emit.
func checkTOMLSafe(s string) error {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("contains control character %q", r)
		}
	}
	return nil
}
