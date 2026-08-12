// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/bitwise-media-group/patchy/internal/artifact"
	"github.com/bitwise-media-group/patchy/internal/cli"
	ctrleval "github.com/bitwise-media-group/patchy/internal/controller/evaluation"
	"github.com/bitwise-media-group/patchy/internal/evalapi"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/runnercfg"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
	"github.com/bitwise-media-group/patchy/internal/version"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

func newServeCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the evaluation reconcilers and the evolve-facing HTTP API",
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), opts) },
	}
	f := cmd.Flags()
	f.String("namespace", "", "namespace the patchy resources live in (default: POD_NAMESPACE)")
	f.String("kubeconfig", "", "kubeconfig path (default: in-cluster config)")
	f.String("health-addr", ":8081", "healthz/readyz probe listen address")
	f.String("auth-config", "", "path to the API auth config (mode none|oidc)")
	f.Int("max-concurrent-units", 4, "evaluation units running at once")
	f.Int("max-units-per-evaluation", 200, "largest accepted submission, in units")
	f.Int("max-submission-bytes", 8<<20, "largest accepted submission body")
	f.Int("max-workspace-bytes", 64<<20, "largest accepted workspace bundle")
	f.Duration("evaluation-ttl", ctrleval.DefaultTTL,
		"retention of finished evaluations (spec.ttlSecondsAfterFinished overrides; 0 keeps forever)")
	f.String("agent-namespace", "patchy-agents", "namespace evaluation Jobs run in")
	f.String("agent-service-account", "patchy-agent", "service account evaluation Jobs run as")
	f.Duration("job-deadline", 2*time.Hour, "activeDeadlineSeconds on evaluation Jobs")
	f.Duration("job-ttl", time.Hour, "ttlSecondsAfterFinished on evaluation Jobs")
	f.String("artifact-base-url", "",
		"artifact endpoint agent pods fetch workspace bundles from (default: the source-controller service, :9790)")
	f.String("artifact-upload-url", "",
		"source-controller internal blob endpoint uploads stream to (default: the source-controller service, :9791)")
	f.String("internal-upload-token-file", "",
		"file holding the shared bearer token for the internal blob endpoint (optional)")
	runnercfg.RegisterEvolveFlags(f)
	return cmd
}

// serviceURL defaults a URL flag to the source-controller in-cluster Service.
func serviceURL(opts *cli.Options, flag, namespace string, port int) string {
	if u := opts.String(flag); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	return fmt.Sprintf("http://patchy-source-controller.%s.svc.cluster.local:%d", namespace, port)
}

// resolveFleet builds the evolve-runner jobs client and the enabled harness
// set (credential Secrets probed in the agents namespace).
func resolveFleet(ctx context.Context, opts *cli.Options, agentNS string,
	runners map[string]jobs.Runner, log *slog.Logger) (*jobs.Client, []string, error) {
	cfg, err := kube.RestConfig(opts.String("kubeconfig"))
	if err != nil {
		return nil, nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	enabled, err := jobs.ResolveRunners(ctx, cs, agentNS, runners, runnercfg.Restrict(opts))
	if err != nil {
		return nil, nil, err
	}
	if len(enabled) == 0 {
		return nil, nil, errors.New(
			"no evolve runner enabled: no configured runner has its credential Secret in the agent namespace")
	}
	runner := jobs.New(cs, jobs.Config{
		Namespace:      agentNS,
		ServiceAccount: opts.String("agent-service-account"),
		Deadline:       opts.Duration("job-deadline"),
		TTL:            opts.Duration("job-ttl"),
		Runners:        runners,
	}, log)
	return runner, enabled, nil
}

// uploadClient builds the client for source-controller's internal blob
// endpoint, reading the optional shared token.
func uploadClient(opts *cli.Options, namespace string) (*artifact.Client, error) {
	uploadToken := ""
	if path := opts.String("internal-upload-token-file"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("internal-upload-token-file: %w", err)
		}
		uploadToken = strings.TrimSpace(string(raw))
	}
	return &artifact.Client{
		BaseURL: serviceURL(opts, "artifact-upload-url", namespace, 9791),
		Token:   uploadToken,
	}, nil
}

// setupReconcilers registers the gate, the unit scheduler, and the TTL loop.
func setupReconcilers(mgr manager.Manager, opts *cli.Options, namespace string, enabled []string,
	runner *jobs.Client, workspaces *artifact.Client, artifactBaseURL string, log *slog.Logger) error {
	gate := &ctrleval.GateReconciler{
		Client:           mgr.GetClient(),
		Namespace:        namespace,
		EnabledHarnesses: enabled,
		Log:              log,
	}
	if err := gate.SetupWithManager(mgr); err != nil {
		return err
	}
	units := &ctrleval.UnitReconciler{
		Client:           mgr.GetClient(),
		Runner:           runner,
		Workspaces:       workspaces,
		Namespace:        namespace,
		MaxConcurrent:    opts.Int("max-concurrent-units"),
		EnabledHarnesses: enabled,
		ArtifactBaseURL:  artifactBaseURL,
		Log:              log,
	}
	if err := units.SetupWithManager(mgr); err != nil {
		return err
	}
	ttl := &ctrleval.TTLReconciler{
		Client:    mgr.GetClient(),
		Namespace: namespace,
		TTL:       opts.Duration("evaluation-ttl"),
		Log:       log,
	}
	return ttl.SetupWithManager(mgr)
}

func serve(ctx context.Context, opts *cli.Options) error {
	prov, shutdown, err := telemetry.Init(ctx, telemetry.Config{
		Dir:            os.Getenv("PATCHY_TELEMETRY_DIR"),
		Level:          opts.LogLevel,
		ServiceName:    "evaluation-controller",
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

	authCfg, err := evalapi.LoadConfig(opts.String("auth-config"))
	if err != nil {
		return err
	}
	authn, err := evalapi.NewAuthenticator(ctx, authCfg)
	if err != nil {
		return err
	}

	runners, err := runnercfg.EvolveRunners(opts)
	if err != nil {
		return err
	}

	agentNS := opts.String("agent-namespace")
	mgr, err := kube.NewManager(kube.Options{
		Kubeconfig:              opts.String("kubeconfig"),
		LeaderElectionID:        "patchy-evaluation-controller-leader",
		LeaderElectionNamespace: namespace,
		Namespaces:              []string{namespace},
		AgentNamespace:          agentNS,
		HealthAddr:              opts.String("health-addr"),
		Log:                     log,
	})
	if err != nil {
		return err
	}

	runner, enabled, err := resolveFleet(ctx, opts, agentNS, runners, log)
	if err != nil {
		return err
	}
	workspaces, err := uploadClient(opts, namespace)
	if err != nil {
		return err
	}
	if err := setupReconcilers(mgr, opts, namespace, enabled, runner, workspaces,
		serviceURL(opts, "artifact-base-url", namespace, 9790), log); err != nil {
		return err
	}

	var granter evalapi.Granter = authz.NewResourceReviewer(
		mgr.GetClient(), namespace, "patchy.bitwisemedia.uk", "evaluations", 0)
	if authCfg.Mode == evalapi.ModeNone {
		granter = evalapi.FullAccess{}
	}
	srv := evalapi.NewServer(mgr.GetClient(), namespace, authn, authCfg.AuthInfo(), granter, workspaces,
		evalapi.Limits{
			MaxUnits:           opts.Int("max-units-per-evaluation"),
			MaxSubmissionBytes: int64(opts.Int("max-submission-bytes")),
			MaxWorkspaceBytes:  int64(opts.Int("max-workspace-bytes")),
		}, log)
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		return srv.StartWatch(ctx, mgr.GetCache())
	})); err != nil {
		return err
	}

	// The HTTP server runs in every replica; only the reconcilers are gated
	// on leader election. Moot at replicas:1 with strategy Recreate, but the
	// API stays up through a lease handover either way.
	httpSrv := &http.Server{
		Addr:              opts.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.LogAttrs(ctx, slog.LevelInfo, "evaluation-controller starting",
		slog.String("namespace", namespace),
		slog.String("agent_namespace", agentNS),
		slog.String("listen_addr", httpSrv.Addr),
		slog.String("auth_mode", authCfg.Mode),
		slog.Any("harnesses", enabled))

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return mgr.Start(ctx) })
	g.Go(func() error {
		if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	})
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
