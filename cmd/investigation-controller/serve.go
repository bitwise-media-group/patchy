// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"

	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/controller/investigation"
	"github.com/bitwise-media-group/patchy/internal/forge"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/schedule"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
	"github.com/bitwise-media-group/patchy/internal/version"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

func newServeCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the investigation gate and the analysis agent scheduler",
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), opts) },
	}
	f := cmd.Flags()
	f.String("namespace", "", "namespace the patchy resources live in (default: POD_NAMESPACE)")
	f.String("kubeconfig", "", "kubeconfig path (default: in-cluster config)")
	f.String("health-addr", ":8081", "healthz/readyz probe listen address")
	f.Duration("finding-min-age", time.Hour, "how old a finding must be before investigation picks it up")
	f.Int("max-attempts", 2, "analysis attempts per finding before it fails")
	f.Int("max-concurrent-investigations", 3, "simultaneously running investigation jobs")
	f.Float64("confidence-threshold", 0.75, "confidence required to queue automated remediation")
	f.Duration("priority-aging-interval", 24*time.Hour, "wait per effective-priority point of aging boost")
	f.Int("priority-aging-cap", 25, "maximum aging boost")

	f.String("agent-image", "", "agent-runner container image (required)")
	f.String("agent-namespace", "patchy-agents", "namespace the agent Jobs run in")
	f.String("agent-service-account", "patchy-agent", "service account for the agent Jobs")
	f.String("anthropic-secret", "patchy-anthropic", "Secret holding the model credential")
	f.String("anthropic-secret-key", "api-key", "key within the model credential Secret")
	f.String("anthropic-secret-env", "ANTHROPIC_API_KEY", "env var the credential is injected as")
	f.Duration("job-deadline", time.Hour, "activeDeadlineSeconds for an agent Job")
	f.Duration("job-ttl", time.Hour, "ttlSecondsAfterFinished for a finished agent Job")
	f.String("model-allowlist", "claude-sonnet-5", "models the investigation may request for remediation")

	f.String("investigate-harness", "claude", "harness the analysis stage runs on")
	f.String("investigate-model", "claude-sonnet-5", "model the analysis stage runs on")
	f.Duration("investigate-timeout", 15*time.Minute, "wall-clock limit for the analysis stage")
	f.Int("investigate-max-turns", 25, "agent turns allowed for the analysis stage")
	f.Int("investigate-token-budget", 150000, "output-token budget for the analysis stage")
	f.Int("remediate-max-turns", 80, "ceiling for the analysis's suggested remediation turns")
	f.Int("remediate-token-budget", 400000, "ceiling for the analysis's suggested remediation budget")
	return cmd
}

// agentEnv is the PATCHY_* configuration every investigation pod receives.
func agentEnv(opts *cli.Options) map[string]string {
	return map[string]string{
		"PATCHY_MODEL_ALLOWLIST": opts.String("model-allowlist"),

		"PATCHY_INVESTIGATE_HARNESS":      opts.String("investigate-harness"),
		"PATCHY_INVESTIGATE_MODEL":        opts.String("investigate-model"),
		"PATCHY_INVESTIGATE_TIMEOUT":      opts.Duration("investigate-timeout").String(),
		"PATCHY_INVESTIGATE_MAX_TURNS":    fmt.Sprint(opts.Int("investigate-max-turns")),
		"PATCHY_INVESTIGATE_TOKEN_BUDGET": fmt.Sprint(opts.Int("investigate-token-budget")),

		"PATCHY_REMEDIATE_MAX_TURNS":    fmt.Sprint(opts.Int("remediate-max-turns")),
		"PATCHY_REMEDIATE_TOKEN_BUDGET": fmt.Sprint(opts.Int("remediate-token-budget")),
	}
}

func serve(ctx context.Context, opts *cli.Options) error {
	prov, shutdown, err := telemetry.Init(ctx, telemetry.Config{
		Dir:            os.Getenv("PATCHY_TELEMETRY_DIR"),
		Level:          opts.LogLevel,
		ServiceName:    "investigation-controller",
		ServiceVersion: version.Version,
	})
	if err != nil {
		prov.Logger.LogAttrs(ctx, slog.LevelWarn, "telemetry disabled", slog.Any("error", err))
	}
	defer func() { _ = shutdown(context.WithoutCancel(ctx)) }()
	log := prov.Logger

	namespace := opts.String("namespace")
	if namespace == "" {
		namespace = os.Getenv("POD_NAMESPACE")
	}
	if namespace == "" {
		return errors.New("namespace is required (--namespace or POD_NAMESPACE)")
	}
	image := opts.String("agent-image")
	if image == "" {
		return errors.New("agent-image is required")
	}

	mgr, err := kube.NewManager(kube.Options{
		Kubeconfig:              opts.String("kubeconfig"),
		LeaderElectionID:        "patchy-investigation-controller-leader",
		LeaderElectionNamespace: namespace,
		Namespaces:              []string{namespace},
		AgentNamespace:          opts.String("agent-namespace"),
		HealthAddr:              opts.String("health-addr"),
		Log:                     log,
	})
	if err != nil {
		return err
	}

	cfg, err := kube.RestConfig(opts.String("kubeconfig"))
	if err != nil {
		return err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("kubernetes clientset: %w", err)
	}
	runner := jobs.New(cs, jobs.Config{
		Namespace:          opts.String("agent-namespace"),
		Image:              image,
		ServiceAccount:     opts.String("agent-service-account"),
		Deadline:           opts.Duration("job-deadline"),
		TTL:                opts.Duration("job-ttl"),
		AnthropicSecret:    opts.String("anthropic-secret"),
		AnthropicSecretKey: opts.String("anthropic-secret-key"),
		AnthropicSecretEnv: opts.String("anthropic-secret-env"),
		Env:                agentEnv(opts),
	}, log)

	gate := &investigation.GateReconciler{
		Client:    mgr.GetClient(),
		Forges:    forge.NewStore(mgr.GetAPIReader()),
		Namespace: namespace,
		MinAge:    opts.Duration("finding-min-age"),
		Parameters: v1alpha1.AgentParameters{
			Model:       opts.String("investigate-model"),
			MaxTurns:    int32(opts.Int("investigate-max-turns")),
			TokenBudget: int64(opts.Int("investigate-token-budget")),
		},
		Log: log,
	}
	if err := gate.SetupWithManager(mgr); err != nil {
		return err
	}
	inv := &investigation.InvestigationReconciler{
		Client:              mgr.GetClient(),
		Runner:              runner,
		Namespace:           namespace,
		MaxConcurrent:       opts.Int("max-concurrent-investigations"),
		MaxAttempts:         int32(opts.Int("max-attempts")),
		ConfidenceThreshold: opts.Float("confidence-threshold"),
		Aging: schedule.AgingPolicy{
			Interval: opts.Duration("priority-aging-interval"),
			Cap:      int32(opts.Int("priority-aging-cap")),
		},
		Log: log,
	}
	if err := inv.SetupWithManager(mgr); err != nil {
		return err
	}

	log.LogAttrs(ctx, slog.LevelInfo, "investigation-controller starting",
		slog.String("namespace", namespace),
		slog.Int("max_concurrent", opts.Int("max-concurrent-investigations")))

	if err := mgr.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
