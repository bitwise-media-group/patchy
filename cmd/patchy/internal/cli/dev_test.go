// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// execDev runs the CLI with args against buffers and no cluster, returning
// captured stdout and the command error.
func execDev(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	opts := &Options{Out: out, ErrOut: errOut, Output: "table", NoColor: true}
	root := NewRoot(opts)
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func TestDevGenericFlagValidation(t *testing.T) {
	t.Chdir(t.TempDir()) // no .patchy.* in reach
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"bad min-severity", []string{"dev", "generic", "--min-severity", "urgent"}, "min-severity"},
		{"secret pair", []string{"dev", "generic", "--secret", "a", "--secret-file", "b"}, "none of the others can be"},
		{"slash in name", []string{"dev", "generic", "--name", "a/b"}, "path segment"},
		{"empty name", []string{"dev", "generic", "--name", ""}, "name is required"},
		{"long name", []string{"dev", "generic", "--name", strings.Repeat("x", 64)}, "63 characters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := execDev(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestDevGenericSecretEnvCollision(t *testing.T) {
	t.Chdir(t.TempDir())
	// Both values arrive without an explicit flag to break the tie: the
	// environment supplies one, the config file the other.
	if err := os.WriteFile(".patchy.yaml", []byte("dev:\n  secret-file: ./s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATCHY_DEV_SECRET", "from-env")
	_, err := execDev(t, "dev", "generic")
	if err == nil || !strings.Contains(err.Error(), "pick one") {
		t.Fatalf("error = %v, want the ambiguous-secret refusal", err)
	}
	if !isUsage(err) {
		t.Errorf("ambiguous secret is not a usage error: %v", err)
	}
}

func TestDevOneShotRequiresURLAndSecret(t *testing.T) {
	t.Chdir(t.TempDir())
	payload := filepath.Join(t.TempDir(), "p.json")
	body := `{"version":"v1","event":"findings","findings":[` +
		`{"repo":{"owner":"a","name":"b"},"alertId":"1","title":"t","severity":"low"}]}`
	if err := os.WriteFile(payload, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := execDev(t, "dev", "enhance", payload)
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("missing url error = %v", err)
	}
	if !isUsage(err) {
		t.Errorf("missing url is not a usage error: %v", err)
	}

	_, err = execDev(t, "dev", "resolve", "--url", "http://127.0.0.1:1/resolve", payload)
	if err == nil || !strings.Contains(err.Error(), "secret is required") {
		t.Fatalf("missing secret error = %v", err)
	}
}

func TestDevOneShotRejectsPingPayload(t *testing.T) {
	t.Chdir(t.TempDir())
	payload := filepath.Join(t.TempDir(), "ping.json")
	if err := os.WriteFile(payload, []byte(`{"version":"v1","event":"ping"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := execDev(t, "dev", "enhance", "--url", "http://127.0.0.1:1/e", "--secret", "s", payload)
	if err == nil || !strings.Contains(err.Error(), "no findings") {
		t.Fatalf("ping payload error = %v, want the no-findings refusal", err)
	}
	if !isUsage(err) {
		t.Errorf("ping payload is not a usage error: %v", err)
	}
}

func TestDevHelpRendersTree(t *testing.T) {
	out, err := execDev(t, "dev", "--help")
	if err != nil {
		t.Fatalf("dev --help: %v", err)
	}
	for _, sub := range []string{"generic", "enhance", "resolve", "PATCHY_DEV_"} {
		if !strings.Contains(out, sub) {
			t.Errorf("dev --help does not mention %q", sub)
		}
	}
}
