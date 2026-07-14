// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func newBound(t *testing.T) (*Options, *cobra.Command) {
	t.Helper()
	o := &Options{LogLevel: new(slog.LevelVar)}
	cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	o.Bind(cmd)
	return o, cmd
}

func TestLoadDefaults(t *testing.T) {
	o, cmd := newBound(t)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := o.Load(cmd); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if o.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want :8080", o.ListenAddr)
	}
	if o.ReconcileInterval != time.Minute {
		t.Errorf("ReconcileInterval = %v, want 1m", o.ReconcileInterval)
	}
}

func TestLoadFlagBeatsEnv(t *testing.T) {
	t.Setenv("PATCHY_LISTEN_ADDR", ":9999")
	o, cmd := newBound(t)
	cmd.SetArgs([]string{"--listen-addr", ":7777"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := o.Load(cmd); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if o.ListenAddr != ":7777" {
		t.Errorf("ListenAddr = %q, want flag value :7777", o.ListenAddr)
	}
}

func TestLoadEnvBeatsDefault(t *testing.T) {
	t.Setenv("PATCHY_RECONCILE_INTERVAL", "5m")
	t.Setenv("PATCHY_GITHUB_APP_ID", "12345")
	o, cmd := newBound(t)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := o.Load(cmd); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if o.ReconcileInterval != 5*time.Minute {
		t.Errorf("ReconcileInterval = %v, want 5m from env", o.ReconcileInterval)
	}
	if o.GitHubAppID != 12345 {
		t.Errorf("GitHubAppID = %d, want 12345 from env", o.GitHubAppID)
	}
}

func TestLogLevel(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		want    slog.Level
		wantErr bool
	}{
		{"default is warn", nil, nil, slog.LevelWarn, false},
		{"flag sets level", []string{"--log-level", "error"}, nil, slog.LevelError, false},
		{"env sets level", nil, map[string]string{"PATCHY_LOG_LEVEL": "info"}, slog.LevelInfo, false},
		{"debug via flag", []string{"--log-level", "debug"}, nil, slog.LevelDebug, false},
		{"unknown level errors", []string{"--log-level", "loud"}, nil, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			o, cmd := newBound(t)
			cmd.SetArgs(tt.args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			err := o.Load(cmd)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load: error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && o.LogLevel.Level() != tt.want {
				t.Errorf("LogLevel = %v, want %v", o.LogLevel.Level(), tt.want)
			}
		})
	}
}

func TestWebhookSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		file    string
		want    string
		wantErr bool
	}{
		{"reads and trims", path, "s3cret", false},
		{"missing flag", "", "", true},
		{"missing file", filepath.Join(dir, "absent"), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &Options{WebhookSecretFile: tt.file}
			got, err := o.WebhookSecret()
			if (err != nil) != tt.wantErr {
				t.Fatalf("WebhookSecret() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && string(got) != tt.want {
				t.Errorf("WebhookSecret() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("empty file", func(t *testing.T) {
		empty := filepath.Join(dir, "empty")
		if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (&Options{WebhookSecretFile: empty}).WebhookSecret(); err == nil {
			t.Error("WebhookSecret() on empty file: error = nil, want error")
		}
	})
}
