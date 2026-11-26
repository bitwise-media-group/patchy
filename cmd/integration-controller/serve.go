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

	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/controller/integration"
	"github.com/bitwise-media-group/patchy/internal/forge"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/repoaccess"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
	"github.com/bitwise-media-group/patchy/internal/tokenauth"
	"github.com/bitwise-media-group/patchy/internal/version"
	"github.com/bitwise-media-group/patchy/internal/webhook"
)

func newServeCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the provider webhook receivers and the Finding projection",
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), opts) },
	}
	f := cmd.Flags()
	f.Duration("accumulation-window", time.Hour,
		"how long alerts of one finding family accumulate into a single finding")
	f.String("namespace", "", "namespace the patchy resources live in (default: POD_NAMESPACE)")
	f.String("kubeconfig", "", "kubeconfig path (default: in-cluster config)")
	f.String("health-addr", ":8081", "healthz/readyz probe listen address")
	f.Int("projection-concurrency", 2,
		"findings projected to tracking issues in parallel (each projection may spend GitHub API requests)")
	f.String("google-oidc-issuer", "",
		"OIDC issuer the Pub/Sub push route verifies tokens against "+
			"(default: Google, the only correct value in production; overridable for e2e)")
	_ = f.MarkHidden("google-oidc-issuer")
	f.String("internal-addr", "",
		"listen address of the in-cluster repository-access and GitHub-user endpoints the status server calls "+
			"(empty disables them)")
	f.String("status-server-service-account", "patchy-status-server",
		"ServiceAccount (in --namespace) whose projected token the internal endpoints accept")
	f.Duration("backfill-request-ttl", integration.DefaultBackfillTTL,
		"how long a finished or undecided BackfillRequest is kept; must outlast the status server's "+
			"--repository-backfill-cooldown")
	return cmd
}

// internalServer builds the in-cluster listener for the repository-access
// and GitHub-user endpoints, or nil when --internal-addr is empty. It is
// kept off the internet-facing webhook listener: only the status server
// calls it, with a projected ServiceAccount token reviewed by the API
// server.
func internalServer(
	opts *cli.Options, namespace string, access *integration.RepositoryAccess, users *integration.GitHubUser,
) (*http.Server, error) {
	addr := opts.String("internal-addr")
	if addr == "" {
		return nil, nil
	}
	restCfg, err := kube.RestConfig(opts.String("kubeconfig"))
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes clientset: %w", err)
	}
	callers := tokenauth.New(cs, tokenauth.Config{
		Audience: repoaccess.Audience,
		Subjects: []string{tokenauth.ServiceAccount(namespace, opts.String("status-server-service-account"))},
	})
	access.Auth, users.Auth = callers, callers
	mux := http.NewServeMux()
	mux.Handle(repoaccess.Path, access.Handler())
	users.Register(mux)
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}, nil
}

func serve(ctx context.Context, opts *cli.Options) error {
	prov, shutdown, err := telemetry.Init(ctx, telemetry.Config{
		Dir:            os.Getenv("PATCHY_TELEMETRY_DIR"),
		Level:          opts.LogLevel,
		ServiceName:    "integration-controller",
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

	mgr, err := kube.NewManager(kube.Options{
		Kubeconfig:              opts.String("kubeconfig"),
		LeaderElectionID:        "patchy-integration-controller-leader",
		LeaderElectionNamespace: namespace,
		Namespaces:              []string{namespace},
		HealthAddr:              opts.String("health-addr"),
		Log:                     log,
	})
	if err != nil {
		return err
	}

	creds := integration.NewCreds(mgr.GetAPIReader())
	ingestor := &integration.Ingestor{
		Client:    mgr.GetClient(),
		Namespace: namespace,
		Window:    opts.Duration("accumulation-window"),
		Log:       log,
	}
	if err := ingestor.SetupWithManager(mgr); err != nil {
		return err
	}
	signals := &integration.Signals{Client: mgr.GetClient(), Namespace: namespace, Log: log}
	receiver := &integration.Receiver{
		Reader:     mgr.GetClient(),
		Creds:      creds,
		Ingest:     ingestor,
		Signals:    signals,
		Namespace:  namespace,
		OIDCIssuer: opts.String("google-oidc-issuer"),
		Log:        log,
	}

	srv, err := webhook.NewServer(webhook.Config{
		Addr:      opts.ListenAddr,
		Endpoints: receiver.Endpoints(),
	}, log)
	if err != nil {
		return err
	}

	ic := &integration.IntegrationReconciler{
		Client: mgr.GetClient(), Creds: creds, Log: log,
		ResetDedup: srv.ResetDedup,
	}
	if err := ic.SetupWithManager(mgr); err != nil {
		return err
	}
	bf := &integration.BackfillReconciler{
		Client: mgr.GetClient(), Creds: creds, Ingest: ingestor, Log: log,
		TTL: opts.Duration("backfill-request-ttl"),
	}
	if err := bf.SetupWithManager(mgr); err != nil {
		return err
	}
	fp := &integration.FindingReconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Creds: creds, Namespace: namespace,
		Concurrency: opts.Int("projection-concurrency"), Log: log,
	}
	if err := fp.SetupWithManager(mgr); err != nil {
		return err
	}

	access := &integration.RepositoryAccess{
		Reader:    mgr.GetClient(),
		Creds:     creds,
		Forges:    forge.NewStore(mgr.GetAPIReader()),
		Namespace: namespace,
		Log:       log,
	}
	users := &integration.GitHubUser{
		Reader:    mgr.GetClient(),
		Creds:     creds,
		Access:    access,
		Namespace: namespace,
		Log:       log,
	}
	internal, err := internalServer(opts, namespace, access, users)
	if err != nil {
		return err
	}

	log.LogAttrs(ctx, slog.LevelInfo, "integration-controller starting",
		slog.String("addr", opts.ListenAddr),
		slog.String("namespace", namespace),
		slog.Duration("accumulation_window", opts.Duration("accumulation-window")))

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return mgr.Start(ctx) })
	g.Go(func() error { return srv.Run(ctx) })
	if internal != nil {
		log.LogAttrs(ctx, slog.LevelInfo, "repository-access endpoint listening", slog.String("addr", internal.Addr))
		g.Go(func() error {
			if err := internal.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})
		g.Go(func() error {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			return internal.Shutdown(shutdownCtx)
		})
	}
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
