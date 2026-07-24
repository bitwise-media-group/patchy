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
	"github.com/bitwise-media-group/patchy/internal/runnercfg"
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

	f.String("agent-namespace", "patchy-agents", "namespace the agent Jobs run in")
	f.String("agent-service-account", "patchy-agent", "service account for the agent Jobs")
	runnercfg.RegisterFlags(f)
	f.Duration("job-deadline", time.Hour, "activeDeadlineSeconds for an agent Job")
	f.Duration("job-ttl", time.Hour, "ttlSecondsAfterFinished for a finished agent Job")
	f.String("model-allowlist", "anthropic/claude-sonnet-5,anthropic/claude-opus-4-8",
		"canonical model ids the investigation may request for remediation")

	f.String("investigate-model", "anthropic/claude-sonnet-5",
		"canonical model id the analysis stage runs on (its harness is derived)")
	f.Duration("investigate-timeout", 15*time.Minute, "wall-clock limit for the analysis stage")
	f.Int("investigate-max-turns", 25, "agent turns allowed for the analysis stage")
	f.Int("investigate-token-budget", 150000, "output-token budget for the analysis stage")
	f.Int("remediate-max-turns", 80, "ceiling for the analysis's suggested remediation turns")
	f.Int("remediate-token-budget", 400000, "ceiling for the analysis's suggested remediation budget")
	return cmd
}

// agentEnv is the PATCHY_* configuration every investigation pod receives.
// The per-Job harness and model are carried on the Job spec, not here.
func agentEnv(opts *cli.Options) map[string]string {
	return map[string]string{
		"PATCHY_MODEL_ALLOWLIST": opts.String("model-allowlist"),

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
	runners, err := runnercfg.Runners(opts)
	if err != nil {
		return err
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

	agentNS := opts.String("agent-namespace")
	investigateModel := opts.String("investigate-model")
	enabled, err := runnercfg.Resolve(ctx, cs, agentNS, runners,
		runnercfg.Restrict(opts), runnercfg.SplitList(opts.String("model-allowlist")), investigateModel)
	if err != nil {
		return err
	}
	investigateHarness, _, err := runnercfg.ResolveHarness(investigateModel, enabled)
	if err != nil {
		return err
	}
	log.LogAttrs(ctx, slog.LevelInfo, "harnesses enabled",
		slog.Any("enabled", enabled), slog.String("investigate_harness", investigateHarness))

	runner := jobs.New(cs, jobs.Config{
		Namespace:      agentNS,
		ServiceAccount: opts.String("agent-service-account"),
		Deadline:       opts.Duration("job-deadline"),
		TTL:            opts.Duration("job-ttl"),
		Runners:        runners,
		Env:            agentEnv(opts),
	}, log)

	gate := &investigation.GateReconciler{
		Client:    mgr.GetClient(),
		Forges:    forge.NewStore(mgr.GetAPIReader()),
		Namespace: namespace,
		MinAge:    opts.Duration("finding-min-age"),
		Parameters: v1alpha1.AgentParameters{
			Model:       investigateModel,
			Harness:     investigateHarness,
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
		InvestigateHarness: investigateHarness,
		InvestigateModel:   investigateModel,
		Log:                log,
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
