// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command agent-runner is the coding-agent runtime inside the ephemeral Job
// pod. It runs one harness stage (investigate or remediate) against the
// repository tree the init container fetched, and reports results as an event
// stream on stdout. It never talks to GitHub.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bitwise-media-group/patchy/internal/agentrun"
	"github.com/bitwise-media-group/patchy/internal/runner"
)

func main() {
	os.Exit(run())
}

func run() int {
	// Diagnostics go to stderr; stdout is reserved for the envelope event
	// stream the controller parses.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := agentrun.FromEnv(os.Getenv)
	if err != nil {
		log.LogAttrs(ctx, slog.LevelError, "invalid configuration", slog.Any("error", err))
		return 2
	}
	cfg.Log = log

	if err := agentrun.New(cfg, &runner.Exec{}).Run(ctx); err != nil {
		// The failure was already emitted as a fatal envelope event. Exit 2
		// so the Job is marked failed for the controller's orphan handling.
		log.LogAttrs(ctx, slog.LevelError, "agent run failed", slog.Any("error", err))
		return 2
	}
	return 0
}
