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
	"github.com/bitwise-media-group/patchy/internal/gcpasset"
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
	f.String("gcp-asset-scope", "",
		"enable the Google Cloud labels enhancer and search assets within this scope "+
			"(organizations/<id>, folders/<id>, or projects/<id>); needs workload identity "+
			"with roles/cloudasset.viewer")
	f.String("gcp-repository-host", "github.com",
		"forge host composed into a repository URL resolved from a resource's org/name labels")
	f.String("gcp-label-org", enhancers.DefaultOrgLabel,
		"resource label naming the owning repository's organization")
	f.String("gcp-label-name", enhancers.DefaultNameLabel,
		"resource label naming the owning repository")
	f.String("gcp-label-provider", enhancers.DefaultProviderLabel,
		"resource label naming the owning repository's forge")
	f.String("gcp-label-url", enhancers.DefaultURLLabel,
		"resource label carrying the owning repository's full URL (supersedes org/name)")
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
	chain, closeChain, err := buildChain(ctx, chainOptions{
		StaticFile:        opts.String("static-context-file"),
		GCPScope:          opts.String("gcp-asset-scope"),
		GCPRepositoryHost: opts.String("gcp-repository-host"),
		GCPLabelKeys: enhancers.LabelKeys{
			Org:      opts.String("gcp-label-org"),
			Name:     opts.String("gcp-label-name"),
			Provider: opts.String("gcp-label-provider"),
			URL:      opts.String("gcp-label-url"),
		},
	})
	if err != nil {
		return err
	}
	defer closeChain()

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
	// GCPScope enables the Google Cloud labels enhancer and bounds its asset
	// search ("organizations/123", "folders/456", "projects/foo").
	GCPScope string
	// GCPLabelKeys overrides the ownership label names.
	GCPLabelKeys enhancers.LabelKeys
	// GCPRepositoryHost is the forge host composed into a resolved URL.
	GCPRepositoryHost string
}

// buildChain assembles the enhancer chain from config. Order matters: the
// first enhancer to resolve a repository wins, so the cloud lookup runs ahead
// of the CMDB, which knows about repositories rather than resources.
func buildChain(ctx context.Context, o chainOptions) ([]enhance.Enhancer, func(), error) {
	var chain []enhance.Enhancer
	cleanup := func() {}

	if o.GCPScope != "" {
		assets, err := gcpasset.New(ctx, o.GCPScope)
		if err != nil {
			return nil, nil, err
		}
		cleanup = func() { _ = assets.Close() }
		gcp, err := enhancers.NewGoogleCloudLabels(enhancers.GoogleCloudOptions{
			Assets:      assets,
			Keys:        o.GCPLabelKeys,
			DefaultHost: o.GCPRepositoryHost,
		})
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		chain = append(chain, gcp)
	}

	if o.StaticFile != "" {
		static, err := enhancers.NewStaticFile(o.StaticFile)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		chain = append(chain, static)
	}

	if len(chain) == 0 {
		chain = append(chain, enhancers.Noop{})
	}
	return chain, cleanup, nil
}
