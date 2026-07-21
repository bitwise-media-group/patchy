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
