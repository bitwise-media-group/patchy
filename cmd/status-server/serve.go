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
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
	"github.com/bitwise-media-group/patchy/internal/version"
	"github.com/bitwise-media-group/patchy/internal/web"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

func newServeCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the status page server",
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), opts) },
	}
	f := cmd.Flags()
	f.String("namespace", "", "namespace the patchy resources live in (default: POD_NAMESPACE)")
	f.String("agent-namespace", "patchy-agents",
		"namespace the agent Jobs run in; live transcripts are followed from their pod logs")
	f.String("kubeconfig", "", "kubeconfig path (default: in-cluster config)")
	f.String("health-addr", ":8081", "healthz/readyz probe listen address")
	f.String("auth-config", "",
		"path to the mounted authentication config; absent means rollup statistics only, no findings access")
	return cmd
}

// agentTailer builds the read-only pod-log reader for the agent namespace.
// It is a plain clientset rather than the manager's client: pod logs are a
// subresource stream, not a cacheable object, and the manager's cache is
// scoped to the patchy namespace anyway.
func agentTailer(kubeconfig, agentNamespace string) (web.Tailer, error) {
	if agentNamespace == "" {
		return nil, errors.New("no agent namespace configured")
	}
	cfg, err := kube.RestConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes clientset: %w", err)
	}
	return jobs.NewTailer(cs, agentNamespace), nil
}

func serve(ctx context.Context, opts *cli.Options) error {
	prov, shutdown, err := telemetry.Init(ctx, telemetry.Config{
		Dir:            os.Getenv("PATCHY_TELEMETRY_DIR"),
		Level:          opts.LogLevel,
		ServiceName:    "status-server",
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

	authCfg, err := auth.LoadConfig(opts.String("auth-config"))
	if err != nil {
		return err
	}
	if authCfg == nil {
		log.LogAttrs(ctx, slog.LevelWarn,
			"authentication not configured; serving rollup statistics only — findings and actions are unavailable")
	}
	authn, err := auth.New(ctx, authCfg, log)
	if err != nil {
		return err
	}

	mgr, err := kube.NewManager(kube.Options{
		Kubeconfig: opts.String("kubeconfig"),
		Namespaces: []string{namespace},
		HealthAddr: opts.String("health-addr"),
		Log:        log,
	})
	if err != nil {
		return err
	}

	// Mode none bypasses access reviews entirely; every other posture asks
	// the cluster per identity.
	var granter web.Granter = authz.NewReviewer(mgr.GetClient(), namespace, 0)
	if authCfg != nil && authCfg.Mode == auth.ModeNone {
		granter = authz.Full{}
	}

	srv := web.NewServer(mgr.GetClient(), namespace, authn, granter, log)
	// Transcripts read through the uncached API reader (caching every
	// ConfigMap in the namespace to serve objects read once per page view is
	// not worth an informer) and follow live runs from the agent namespace's
	// pod logs, via the API server — the status server never dials an agent pod.
	agentNS := opts.String("agent-namespace")
	tailer, err := agentTailer(opts.String("kubeconfig"), agentNS)
	if err != nil {
		// Live following is a nicety; persisted transcripts still serve.
		log.LogAttrs(ctx, slog.LevelWarn, "live transcripts unavailable",
			slog.String("agent_namespace", agentNS), slog.Any("error", err))
	}
	srv = srv.WithTranscripts(mgr.GetAPIReader(), tailer)

	err = mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		return srv.StartWatch(ctx, mgr.GetCache())
	}))
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              opts.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	mode := "unconfigured"
	if authCfg != nil {
		mode = authCfg.Mode
	}
	log.LogAttrs(ctx, slog.LevelInfo, "status-server starting",
		slog.String("namespace", namespace),
		slog.String("listen_addr", httpSrv.Addr),
		slog.String("auth_mode", mode))

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
