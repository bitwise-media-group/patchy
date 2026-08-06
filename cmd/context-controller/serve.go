// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/internal/cli"
	ctxctrl "github.com/bitwise-media-group/patchy/internal/controller/context"
	"github.com/bitwise-media-group/patchy/internal/enhancers"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
	"github.com/bitwise-media-group/patchy/internal/version"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
)

func newServeCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the enhancer chain over freshly opened findings",
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), opts) },
	}
	f := cmd.Flags()
	f.String("namespace", "", "namespace the patchy resources live in (default: POD_NAMESPACE)")
	f.String("kubeconfig", "", "kubeconfig path (default: in-cluster config)")
	f.String("health-addr", ":8081", "healthz/readyz probe listen address")
	f.String("static-context-file", "",
		"YAML file mapping repositories to owners/attributes (the fake-CMDB enhancer)")
	return cmd
}

func serve(ctx context.Context, opts *cli.Options) error {
	prov, shutdown, err := telemetry.Init(ctx, telemetry.Config{
		Dir:            os.Getenv("PATCHY_TELEMETRY_DIR"),
		Level:          opts.LogLevel,
		ServiceName:    "context-controller",
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
		LeaderElectionID:        "patchy-context-controller-leader",
		LeaderElectionNamespace: namespace,
		Namespaces:              []string{namespace},
		HealthAddr:              opts.String("health-addr"),
		Log:                     log,
	})
	if err != nil {
		return err
	}

	// The chain needs the manager's cached client: the dynamic enhancers
	// read their configuration off their Integrations per enhancement, so a
	// spec change takes effect without a restart. The generic source also
	// takes the API reader — signing secrets are never cached.
	chain, closeChain, err := buildChain(chainOptions{
		StaticFile: opts.String("static-context-file"),
		Assets:     ctxctrl.AssetConfigSource(mgr.GetClient(), namespace),
		AWSTags:    ctxctrl.AWSTagsConfigSource(mgr.GetClient(), namespace),
		AzureTags:  ctxctrl.AzureTagsConfigSource(mgr.GetClient(), namespace),
		Generic:    ctxctrl.GenericEnhancerConfigSource(mgr.GetClient(), mgr.GetAPIReader(), namespace, log),
	})
	if err != nil {
		return err
	}
	defer closeChain()

	fc := &ctxctrl.FindingReconciler{Client: mgr.GetClient(), Enhancers: chain, Log: log}
	if err := fc.SetupWithManager(mgr); err != nil {
		return err
	}

	log.LogAttrs(ctx, slog.LevelInfo, "context-controller starting",
		slog.String("namespace", namespace),
		slog.Int("enhancers", len(chain)))

	if err := mgr.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// chainOptions are the enhancer chain's configuration.
type chainOptions struct {
	// StaticFile is the fake-CMDB map, when configured.
	StaticFile string
	// Assets reads the Cloud Asset Inventory capability off the Integration.
	Assets enhancers.ConfigSource
	// AWSTags reads the AWS resource-tags capability off the Integration.
	AWSTags enhancers.AWSConfigSource
	// AzureTags reads the Azure resource-tags capability off the Integration.
	AzureTags enhancers.AzureConfigSource
	// Generic reads every enabled generic enhancer endpoint.
	Generic enhancers.GenericConfigSource
}

// buildChain assembles the enhancer chain. Order matters: the first enhancer
// to resolve a repository wins, so the cloud lookups run ahead of the CMDB,
// which knows about repositories rather than resources (the cloud enhancers
// are disjoint by provider, so their order is immaterial); the generic
// fan-out runs after them — the platform's own inventory outranks an
// external process on repository resolution — and ahead of the static file.
// The dynamic enhancers are always in the chain — whether they act is their
// Integrations' decision, read per enhancement.
func buildChain(o chainOptions) ([]enhance.Enhancer, func(), error) {
	gcp := &enhancers.DynamicGoogleCloud{Config: o.Assets}
	aws := &enhancers.DynamicAWS{Config: o.AWSTags}
	azure := &enhancers.DynamicAzure{Config: o.AzureTags}
	chain := []enhance.Enhancer{gcp, aws, azure, &enhancers.DynamicGeneric{Configs: o.Generic}}
	cleanup := func() {
		_ = gcp.Close()
		_ = aws.Close()
		_ = azure.Close()
	}

	if o.StaticFile != "" {
		static, err := enhancers.NewStaticFile(o.StaticFile)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		chain = append(chain, static)
	}
	return chain, cleanup, nil
}
