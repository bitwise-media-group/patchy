// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// parseDevGeneric builds the harness command and parses args, without
// running it, so loadDevConfig can be exercised in isolation.
func parseDevGeneric(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := newDevGenericCmd(&Options{})
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	return cmd
}

func TestLoadDevConfigPrecedence(t *testing.T) {
	t.Chdir(t.TempDir())
	config := "dev:\n" +
		"  addr: file-addr:1\n" +
		"  name: file-name\n" +
		"  min-severity: high\n" +
		"  enhance-timeout: 5s\n"
	if err := os.WriteFile(".patchy.yaml", []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATCHY_DEV_NAME", "env-name")

	cmd := parseDevGeneric(t, "--addr", "flag-addr:1")
	v, err := loadDevConfig(cmd, devGenericKeys)
	if err != nil {
		t.Fatalf("loadDevConfig: %v", err)
	}

	if got := v.GetString("dev.addr"); got != "flag-addr:1" {
		t.Errorf("addr = %q, want the explicit flag to win over the file", got)
	}
	if got := v.GetString("dev.name"); got != "env-name" {
		t.Errorf("name = %q, want the environment to win over the file", got)
	}
	if got := v.GetString("dev.min-severity"); got != "high" {
		t.Errorf("min-severity = %q, want the file to win over the default", got)
	}
	if got := v.GetDuration("dev.enhance-timeout"); got.Seconds() != 5 {
		t.Errorf("enhance-timeout = %v, want the file's 5s", got)
	}
	if got := v.GetString("dev.enhance-url"); got != "" {
		t.Errorf("enhance-url = %q, want the untouched default", got)
	}
	if v.ConfigFileUsed() == "" {
		t.Error("ConfigFileUsed() is empty; provenance narration would be silent")
	}
}

func TestLoadDevConfigAmbiguousFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{".patchy.yaml", ".patchy.json"} {
		if err := os.WriteFile(name, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := loadDevConfig(parseDevGeneric(t), devGenericKeys)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v, want the ambiguity refusal", err)
	}
	if !isUsage(err) {
		t.Errorf("ambiguous config is not a usage error: %v", err)
	}
}

func TestLoadDevConfigMalformedFile(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".patchy.yaml", []byte("dev: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadDevConfig(parseDevGeneric(t), devGenericKeys)
	if err == nil || !strings.Contains(err.Error(), ".patchy.yaml") {
		t.Fatalf("error = %v, want a parse failure naming the file", err)
	}
}

func TestResolveDevSecretFlagBreaksTie(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("secret.txt", []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Explicit --secret wins over a secret-file arriving from config.
	cmd := parseDevGeneric(t, "--secret", "flag-secret")
	got, _, err := resolveDevSecret(cmd, "flag-secret", "secret.txt")
	if err != nil || string(got) != "flag-secret" {
		t.Errorf("resolveDevSecret(flag set) = %q, %v; want the flag value", got, err)
	}

	// Explicit --secret-file wins the other way, and trailing newlines are
	// never part of the key.
	cmd = parseDevGeneric(t, "--secret-file", "secret.txt")
	got, provenance, err := resolveDevSecret(cmd, "env-secret", "secret.txt")
	if err != nil || string(got) != "file-secret" {
		t.Errorf("resolveDevSecret(file flag set) = %q, %v; want the trimmed file", got, err)
	}
	if !strings.Contains(provenance, "secret.txt") {
		t.Errorf("provenance = %q, want it to name the file", provenance)
	}

	// Neither resolved: nil without error — the caller decides what that means.
	got, _, err = resolveDevSecret(parseDevGeneric(t), "", "")
	if err != nil || got != nil {
		t.Errorf("resolveDevSecret(empty) = %q, %v; want nil, nil", got, err)
	}
}
