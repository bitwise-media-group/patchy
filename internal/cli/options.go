// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// envPrefix namespaces every environment variable: flag --listen-addr is
// PATCHY_LISTEN_ADDR, and so on (dashes become underscores).
const envPrefix = "PATCHY"

// NewOptions builds Options with the conventional stderr logger and shared
// verbosity level every binary starts from.
func NewOptions() *Options {
	level := new(slog.LevelVar)
	return &Options{
		Log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})),
		LogLevel: level,
	}
}

// NewControllerRoot builds a controller's root command with the shared
// options bound and flag/env resolution wired into PersistentPreRunE.
func NewControllerRoot(name, short string, opts *Options) *cobra.Command {
	root := NewRoot(name, short)
	opts.Bind(root)
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error { return opts.Load(cmd) }
	return root
}

// Options carries the configuration shared by every controller binary.
// Precedence: explicit flag > PATCHY_* environment > default.
type Options struct {
	// Log is the process logger; main constructs it, telemetry may replace
	// it with a fanout after Init.
	Log *slog.Logger
	// LogLevel is set from --log-level before any command runs.
	LogLevel *slog.LevelVar

	// ListenAddr is the webhook/health HTTP listen address.
	ListenAddr string
	// WebhookSecretFile names a file holding the GitHub webhook shared
	// secret (mounted from a Kubernetes Secret).
	WebhookSecretFile string
	// GitHubAppID and GitHubAppKeyFile configure GitHub App authentication —
	// the production mode.
	GitHubAppID      int64
	GitHubAppKeyFile string
	// GitHubToken is the dev-mode personal-access-token fallback; set, it
	// wins over App auth.
	GitHubToken string
	// GitHubBaseURL overrides the API endpoint (GHES, tests).
	GitHubBaseURL string
	// ReconcileInterval paces the controller's reconcile loop.
	ReconcileInterval time.Duration
	// LogLevelName is the configured level: debug, info, warn, or error.
	LogLevelName string

	viper *viper.Viper
}

// Bind registers the shared persistent flags on cmd and wires the viper
// environment binding. Call once from each binary's root command setup;
// controller-specific flags bind on top with BindExtra.
func (o *Options) Bind(cmd *cobra.Command) {
	pf := cmd.PersistentFlags()
	pf.StringVar(&o.ListenAddr, "listen-addr", ":8080", "webhook/health HTTP listen address")
	pf.StringVar(&o.WebhookSecretFile, "webhook-secret-file", "", "file containing the GitHub webhook secret")
	pf.Int64Var(&o.GitHubAppID, "github-app-id", 0, "GitHub App ID (App auth)")
	pf.StringVar(&o.GitHubAppKeyFile, "github-app-private-key-file", "", "PEM file with the GitHub App private key")
	pf.StringVar(&o.GitHubToken, "github-token", "", "personal access token (dev fallback; wins over App auth)")
	pf.StringVar(&o.GitHubBaseURL, "github-base-url", "", "GitHub API base URL (default api.github.com)")
	pf.DurationVar(&o.ReconcileInterval, "reconcile-interval", time.Minute, "reconcile loop interval")
	pf.StringVar(&o.LogLevelName, "log-level", "warn", "log level: debug, info, warn, or error")

	o.viper = viper.New()
	o.viper.SetEnvPrefix(envPrefix)
	o.viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	o.viper.AutomaticEnv()
}

// Load resolves flag/env precedence into the Options fields. Run it in
// PersistentPreRunE, after cobra parsed the flags.
func (o *Options) Load(cmd *cobra.Command) error {
	if err := o.viper.BindPFlags(cmd.Flags()); err != nil {
		return fmt.Errorf("bind flags: %w", err)
	}
	// Viper resolves precedence (set flag > env > flag default); copy the
	// results back into the typed fields the rest of the process reads.
	o.ListenAddr = o.viper.GetString("listen-addr")
	o.WebhookSecretFile = o.viper.GetString("webhook-secret-file")
	o.GitHubAppID = o.viper.GetInt64("github-app-id")
	o.GitHubAppKeyFile = o.viper.GetString("github-app-private-key-file")
	o.GitHubToken = o.viper.GetString("github-token")
	o.GitHubBaseURL = o.viper.GetString("github-base-url")
	o.ReconcileInterval = o.viper.GetDuration("reconcile-interval")
	o.LogLevelName = o.viper.GetString("log-level")
	var level slog.Level
	if err := level.UnmarshalText([]byte(o.LogLevelName)); err != nil {
		return fmt.Errorf("log level: %q is not debug, info, warn, or error", o.LogLevelName)
	}
	if o.LogLevel != nil {
		o.LogLevel.Set(level)
	}
	return nil
}

// String reads an extra controller-specific value bound via cmd flags,
// applying the same flag/env precedence.
func (o *Options) String(key string) string { return o.viper.GetString(key) }

// Duration reads an extra controller-specific duration value.
func (o *Options) Duration(key string) time.Duration { return o.viper.GetDuration(key) }

// Float reads an extra controller-specific float value.
func (o *Options) Float(key string) float64 { return o.viper.GetFloat64(key) }

// Int reads an extra controller-specific integer value.
func (o *Options) Int(key string) int { return o.viper.GetInt(key) }

// WebhookSecret loads the webhook secret from WebhookSecretFile, trimmed of
// the trailing newline editors and Secret mounts add.
func (o *Options) WebhookSecret() ([]byte, error) {
	if o.WebhookSecretFile == "" {
		return nil, fmt.Errorf("webhook secret: --webhook-secret-file (or %s_WEBHOOK_SECRET_FILE) is required", envPrefix)
	}
	raw, err := os.ReadFile(o.WebhookSecretFile)
	if err != nil {
		return nil, fmt.Errorf("webhook secret: %w", err)
	}
	secret := strings.TrimRight(string(raw), "\r\n")
	if secret == "" {
		return nil, fmt.Errorf("webhook secret: %s is empty", o.WebhookSecretFile)
	}
	return []byte(secret), nil
}
