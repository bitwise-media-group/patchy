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
	"github.com/bitwise-media-group/patchy/internal/controller/remediation"
	"github.com/bitwise-media-group/patchy/internal/controller/rollup"
	"github.com/bitwise-media-group/patchy/internal/forge"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/priority"
	"github.com/bitwise-media-group/patchy/internal/runnercfg"
	"github.com/bitwise-media-group/patchy/internal/schedule"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
	"github.com/bitwise-media-group/patchy/internal/version"
)

func newServeCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run queue admission, the remediation agent scheduler, and the rollup/TTL loop",
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), opts) },
	}
	f := cmd.Flags()
	f.String("namespace", "", "namespace the patchy resources live in (default: POD_NAMESPACE)")
	f.String("kubeconfig", "", "kubeconfig path (default: in-cluster config)")
	f.String("health-addr", ":8081", "healthz/readyz probe listen address")
	f.Int("max-attempts", 2, "remediation attempts per finding before it fails")
	f.Int("max-concurrent-remediations", 1, "simultaneously running remediation jobs")
	f.Duration("priority-aging-interval", 24*time.Hour, "wait per effective-priority point of aging boost")
	f.Int("priority-aging-cap", 25, "maximum aging boost")
	f.Float64("priority-weight-severity", priority.DefaultWeights.Severity,
		"scheduling-priority weight of the scanner severity")
	f.Float64("priority-weight-exploitability", priority.DefaultWeights.Exploitability,
		"scheduling-priority weight of the assessed exploitability")
	f.Float64("priority-weight-likelihood", priority.DefaultWeights.Likelihood,
		"scheduling-priority weight of the assessed likelihood")
	f.Float64("priority-weight-impact", priority.DefaultWeights.Impact,
		"scheduling-priority weight of the assessed impact")
	f.Duration("finding-ttl", rollup.DefaultTTL,
		"how long completed findings are kept before deletion; 0 keeps them forever")

	f.String("agent-namespace", "patchy-agents", "namespace the agent Jobs run in")
	f.String("agent-service-account", "patchy-agent", "service account for the agent Jobs")
	runnercfg.RegisterFlags(f)
	f.Duration("job-deadline", time.Hour, "activeDeadlineSeconds for an agent Job")
	f.Duration("job-ttl", time.Hour, "ttlSecondsAfterFinished for a finished agent Job")

	f.String("model-allowlist", "anthropic/claude-sonnet-5,anthropic/claude-opus-4-8",
		"canonical model ids remediation may run (the investigation's choice is clamped to this)")
	f.String("remediate-model", "anthropic/claude-sonnet-5",
		"canonical default model when the report's suggestion is missing or unusable (its harness is derived)")
	f.Duration("remediate-timeout", 45*time.Minute, "wall-clock limit for the remediation stage")
	f.Int("remediate-max-turns", 80, "agent turns allowed for the remediation stage")
	f.Int("remediate-token-budget", 400000, "output-token budget for the remediation stage")
	return cmd
}

// agentEnv is the PATCHY_* configuration every remediation pod receives. The
// per-Job harness and model are carried on the Job spec, not here; the
// allowlist is enforced controller-side (the spawner), so the pod does not
// need it.
func agentEnv(opts *cli.Options) map[string]string {
	return map[string]string{
		"PATCHY_REMEDIATE_TIMEOUT":      opts.Duration("remediate-timeout").String(),
		"PATCHY_REMEDIATE_MAX_TURNS":    fmt.Sprint(opts.Int("remediate-max-turns")),
		"PATCHY_REMEDIATE_TOKEN_BUDGET": fmt.Sprint(opts.Int("remediate-token-budget")),
	}
}

func serve(ctx context.Context, opts *cli.Options) error {
	prov, shutdown, err := telemetry.Init(ctx, telemetry.Config{
		Dir:            os.Getenv("PATCHY_TELEMETRY_DIR"),
		Level:          opts.LogLevel,
		ServiceName:    "remediation-controller",
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
		LeaderElectionID:        "patchy-remediation-controller-leader",
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
	remediateModel := opts.String("remediate-model")
	allowlist := runnercfg.SplitList(opts.String("model-allowlist"))
	enabled, err := runnercfg.Resolve(ctx, cs, agentNS, runners, runnercfg.Restrict(opts), allowlist, remediateModel)
	if err != nil {
		return err
	}
	log.LogAttrs(ctx, slog.LevelInfo, "harnesses enabled",
		slog.Any("enabled", enabled), slog.String("default_remediate_model", remediateModel))

	runner := jobs.New(cs, jobs.Config{
		Namespace:      agentNS,
		ServiceAccount: opts.String("agent-service-account"),
		Deadline:       opts.Duration("job-deadline"),
		TTL:            opts.Duration("job-ttl"),
		Runners:        runners,
		Env:            agentEnv(opts),
	}, log)

	forges := forge.NewStore(mgr.GetAPIReader())
	spawner := &remediation.SpawnerReconciler{
		Client:    mgr.GetClient(),
		Namespace: namespace,
		Weights: priority.Weights{
			Severity:       opts.Float("priority-weight-severity"),
			Exploitability: opts.Float("priority-weight-exploitability"),
			Likelihood:     opts.Float("priority-weight-likelihood"),
			Impact:         opts.Float("priority-weight-impact"),
		},
		Enabled:      enabled,
		Allowlist:    allowlist,
		DefaultModel: remediateModel,
		Log:          log,
	}
	if err := spawner.SetupWithManager(mgr); err != nil {
		return err
	}
	rem := &remediation.RemediationReconciler{
		Client:        mgr.GetClient(),
		Runner:        runner,
		Forge:         remediation.NewForgeWriter(forges),
		Namespace:     namespace,
		MaxConcurrent: opts.Int("max-concurrent-remediations"),
		MaxAttempts:   int32(opts.Int("max-attempts")),
		Aging: schedule.AgingPolicy{
			Interval: opts.Duration("priority-aging-interval"),
			Cap:      int32(opts.Int("priority-aging-cap")),
		},
		Enabled: enabled,
		Log:     log,
	}
	if err := rem.SetupWithManager(mgr); err != nil {
		return err
	}
	roll := &rollup.Reconciler{
		Client:    mgr.GetClient(),
		Namespace: namespace,
		TTL:       opts.Duration("finding-ttl"),
		Log:       log,
	}
	if err := roll.SetupWithManager(mgr); err != nil {
		return err
	}

	log.LogAttrs(ctx, slog.LevelInfo, "remediation-controller starting",
		slog.String("namespace", namespace),
		slog.Int("max_concurrent", opts.Int("max-concurrent-remediations")),
		slog.Duration("finding_ttl", opts.Duration("finding-ttl")))

	if err := mgr.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
