// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/controller/integration"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
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
	return cmd
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
	signals := &integration.Signals{Client: mgr.GetClient(), Namespace: namespace, Log: log}
	receiver := &integration.Receiver{
		Reader:    mgr.GetClient(),
		Creds:     creds,
		Ingest:    ingestor,
		Signals:   signals,
		Namespace: namespace,
		Log:       log,
	}

	srv := webhook.NewServer(webhook.Config{
		Addr:       opts.ListenAddr,
		Path:       "/github/webhooks",
		SecretsFor: receiver.Secrets,
	}, log, receiver)

	ic := &integration.IntegrationReconciler{
		Client: mgr.GetClient(), Creds: creds, Log: log,
		ResetDedup: srv.ResetDedup,
	}
	if err := ic.SetupWithManager(mgr); err != nil {
		return err
	}
	fp := &integration.FindingReconciler{Client: mgr.GetClient(), Creds: creds, Namespace: namespace, Log: log}
	if err := fp.SetupWithManager(mgr); err != nil {
		return err
	}

	log.LogAttrs(ctx, slog.LevelInfo, "integration-controller starting",
		slog.String("addr", opts.ListenAddr),
		slog.String("namespace", namespace),
		slog.Duration("accumulation_window", opts.Duration("accumulation-window")))

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return mgr.Start(ctx) })
	g.Go(func() error { return srv.Run(ctx) })
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
