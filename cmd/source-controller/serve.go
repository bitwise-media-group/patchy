// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/bitwise-media-group/patchy/internal/artifact"
	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/controller/source"
	"github.com/bitwise-media-group/patchy/internal/forge"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
	"github.com/bitwise-media-group/patchy/internal/version"
)

func newServeCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Forge and Repository reconcilers and the artifact server",
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), opts) },
	}
	f := cmd.Flags()
	f.String("namespace", "", "namespace the patchy resources live in (default: POD_NAMESPACE)")
	f.String("kubeconfig", "", "kubeconfig path (default: in-cluster config)")
	f.String("health-addr", ":8081", "healthz/readyz probe listen address")
	f.String("artifact-addr", ":9790", "listen address of the artifact server")
	f.String("artifact-base-url", "",
		"base URL agent pods fetch artifacts from (default: the in-cluster service address)")
	f.String("artifact-dir", "/data/artifacts", "directory the artifact tarballs are stored in")
	f.Int("max-artifact-bytes", int(source.DefaultMaxArtifactBytes),
		"largest repository tarball stored; larger repositories stall")
	return cmd
}

// artifactBaseURL is the URL minted into Repository statuses; unless
// configured it is the controller's in-cluster Service address on the
// artifact port.
func artifactBaseURL(opts *cli.Options, namespace string) (string, error) {
	if u := opts.String("artifact-base-url"); u != "" {
		return u, nil
	}
	_, port, err := net.SplitHostPort(opts.String("artifact-addr"))
	if err != nil {
		return "", fmt.Errorf("artifact-addr: %w", err)
	}
	return fmt.Sprintf("http://patchy-source-controller.%s.svc.cluster.local:%s", namespace, port), nil
}

func serve(ctx context.Context, opts *cli.Options) error {
	prov, shutdown, err := telemetry.Init(ctx, telemetry.Config{
		Dir:            os.Getenv("PATCHY_TELEMETRY_DIR"),
		Level:          opts.LogLevel,
		ServiceName:    "source-controller",
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
	baseURL, err := artifactBaseURL(opts, namespace)
	if err != nil {
		return err
	}
	store, err := artifact.NewStore(opts.String("artifact-dir"), baseURL)
	if err != nil {
		return err
	}

	mgr, err := kube.NewManager(kube.Options{
		Kubeconfig:              opts.String("kubeconfig"),
		LeaderElectionID:        "patchy-source-controller-leader",
		LeaderElectionNamespace: namespace,
		Namespaces:              []string{namespace},
		HealthAddr:              opts.String("health-addr"),
		Log:                     log,
	})
	if err != nil {
		return err
	}

	forges := forge.NewStore(mgr.GetAPIReader())
	fc := &source.ForgeReconciler{Client: mgr.GetClient(), Forges: forges, Log: log}
	if err := fc.SetupWithManager(mgr); err != nil {
		return err
	}
	rc := &source.RepositoryReconciler{
		Client:           mgr.GetClient(),
		Forges:           forges,
		Artifacts:        store,
		MaxArtifactBytes: int64(opts.Int("max-artifact-bytes")),
		Log:              log,
	}
	if err := rc.SetupWithManager(mgr); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              opts.String("artifact-addr"),
		Handler:           store.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.LogAttrs(ctx, slog.LevelInfo, "source-controller starting",
		slog.String("namespace", namespace),
		slog.String("artifact_addr", srv.Addr),
		slog.String("artifact_base_url", baseURL))

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return mgr.Start(ctx) })
	g.Go(func() error {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
