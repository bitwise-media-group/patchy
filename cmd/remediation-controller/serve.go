// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/ghpush"
	"github.com/bitwise-media-group/patchy/internal/harness"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/reconcile"
	"github.com/bitwise-media-group/patchy/internal/remedctrl"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
	"github.com/bitwise-media-group/patchy/internal/version"
	"github.com/bitwise-media-group/patchy/internal/webhook"
)

func newServeCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the webhook receiver and the remediation reconcile loop",
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), opts) },
	}
	f := cmd.Flags()
	f.Duration("issue-min-age", time.Hour, "how old a finding must be before remediation picks it up")
	f.Int("max-attempts", 2, "agent job attempts per finding before it is handed to a human")
	f.Float64("confidence-threshold", 0.75, "classification confidence required for automated remediation")
	f.String("agent-image", "", "agent-runner container image (required)")
	f.String("agent-namespace", "patchy-agents", "namespace the agent Jobs run in")
	f.String("agent-service-account", "patchy-agent", "service account for the agent Jobs")
	f.String("anthropic-secret", "patchy-anthropic", "Secret holding the model credential")
	f.String("anthropic-secret-key", "api-key", "key within the model credential Secret")
	f.String("anthropic-secret-env", "ANTHROPIC_API_KEY",
		"env var the credential is injected as: ANTHROPIC_API_KEY for an API key, "+
			"or CLAUDE_CODE_OAUTH_TOKEN for a `claude setup-token` OAuth token")
	f.Duration("job-deadline", time.Hour, "activeDeadlineSeconds for an agent Job")
	f.Duration("job-ttl", time.Hour, "ttlSecondsAfterFinished for a finished agent Job")
	f.String("model-allowlist", "claude-sonnet-5", "models the classifier may request for remediation")
	f.String("kubeconfig", "", "kubeconfig path (default: in-cluster config)")

	// The two agent stages are configured symmetrically and independently
	// (DESIGN keeps the classification and remediation harnesses separable).
	// The classification stage runs on exactly these limits; the remediation
	// ones are ceilings the classification's own request is clamped to.
	f.String("classify-harness", "claude", "harness the classification stage runs on")
	f.String("classify-model", "claude-sonnet-5", "model the classification stage runs on")
	f.Duration("classify-timeout", 15*time.Minute, "wall-clock limit for the classification stage")
	f.Int("classify-max-turns", 25, "agent turns allowed for the classification stage")
	f.Int("classify-token-budget", 150000, "output-token budget for the classification stage")

	f.String("remediate-harness", "claude", "harness the remediation stage runs on")
	f.String("remediate-model", "claude-sonnet-5", "model the remediation stage runs on by default")
	f.Duration("remediate-timeout", 45*time.Minute, "wall-clock limit for the remediation stage")
	f.Int("remediate-max-turns", 80, "upper bound on the turns a classification may request")
	f.Int("remediate-token-budget", 400000, "upper bound on the output-token budget a classification may request")
	return cmd
}

// agentEnv is the PATCHY_* configuration the controller passes through to
// every agent pod. The runner reads these via agentrun.FromEnv, so the
// controller's flags are the single place an operator configures the agent.
func agentEnv(opts *cli.Options) map[string]string {
	return map[string]string{
		"PATCHY_MODEL_ALLOWLIST":      opts.String("model-allowlist"),
		"PATCHY_CONFIDENCE_THRESHOLD": fmt.Sprint(opts.Float("confidence-threshold")),

		"PATCHY_CLASSIFY_HARNESS":      opts.String("classify-harness"),
		"PATCHY_CLASSIFY_MODEL":        opts.String("classify-model"),
		"PATCHY_CLASSIFY_TIMEOUT":      opts.Duration("classify-timeout").String(),
		"PATCHY_CLASSIFY_MAX_TURNS":    fmt.Sprint(opts.Int("classify-max-turns")),
		"PATCHY_CLASSIFY_TOKEN_BUDGET": fmt.Sprint(opts.Int("classify-token-budget")),

		"PATCHY_REMEDIATE_HARNESS":      opts.String("remediate-harness"),
		"PATCHY_REMEDIATE_MODEL":        opts.String("remediate-model"),
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

	image := opts.String("agent-image")
	if image == "" {
		return errors.New("--agent-image (or PATCHY_AGENT_IMAGE) is required")
	}
	secretEnv := opts.String("anthropic-secret-env")
	if !slices.Contains(credentialEnvKeys(), secretEnv) {
		return fmt.Errorf("--anthropic-secret-env %q is not a credential env var any harness accepts (one of: %s)",
			secretEnv, strings.Join(credentialEnvKeys(), ", "))
	}
	secret, err := opts.WebhookSecret()
	if err != nil {
		return err
	}
	resolver, err := opts.GitHub()
	if err != nil {
		return err
	}
	kube, err := kubeClient(opts.String("kubeconfig"))
	if err != nil {
		return err
	}

	runner := jobs.New(kube, jobs.Config{
		Namespace:          opts.String("agent-namespace"),
		Image:              image,
		ServiceAccount:     opts.String("agent-service-account"),
		Deadline:           opts.Duration("job-deadline"),
		TTL:                opts.Duration("job-ttl"),
		AnthropicSecret:    opts.String("anthropic-secret"),
		AnthropicSecretKey: opts.String("anthropic-secret-key"),
		AnthropicSecretEnv: secretEnv,
		Env:                agentEnv(opts),
	}, log)

	ctrl := remedctrl.New(log, clients{resolver}, runner, ghpush.New(opts.GitHubBaseURL), remedctrl.Config{
		MinAge:      opts.Duration("issue-min-age"),
		MaxAttempts: opts.Int("max-attempts"),
	})
	srv := webhook.NewServer(webhook.Config{Addr: opts.ListenAddr, Secret: secret}, log, ctrl)

	log.LogAttrs(ctx, slog.LevelInfo, "remediation-controller starting",
		slog.String("addr", opts.ListenAddr),
		slog.String("agent_image", image),
		slog.String("agent_namespace", opts.String("agent-namespace")),
		slog.Duration("reconcile_interval", opts.ReconcileInterval))

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return srv.Run(ctx) })
	g.Go(func() error {
		return reconcile.Run(ctx, log, "remediation-controller", opts.ReconcileInterval, ctrl.Reconcile)
	})
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// credentialEnvKeys is the union of the credential env vars the builtin
// harnesses accept, in registry order — the legal values for
// --anthropic-secret-env.
func credentialEnvKeys() []string {
	var keys []string
	for _, h := range harness.All() {
		for _, k := range h.EnvKeys() {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	return keys
}

// kubeClient builds the Kubernetes client: in-cluster by default, from a
// kubeconfig when one is named (local development).
func kubeClient(kubeconfig string) (kubernetes.Interface, error) {
	var cfg *rest.Config
	var err error
	if kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		cfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}
	return kubernetes.NewForConfig(cfg)
}
