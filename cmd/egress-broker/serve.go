// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes"

	"github.com/bitwise-media-group/patchy/internal/broker"
	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
	"github.com/bitwise-media-group/patchy/internal/version"
)

func newServeCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the egress credential broker",
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), opts) },
	}
	f := cmd.Flags()
	f.String("kubeconfig", "", "kubeconfig path (default: in-cluster config)")
	f.String("health-addr", ":8081", "healthz/readyz probe listen address")
	f.String("token-audience", broker.DefaultAudience, "audience callers' projected tokens must be bound to")
	f.Duration("verdict-ttl", broker.DefaultVerdictTTL, "how long one caller token's TokenReview verdict is cached")
	f.Duration("sse-ping-interval", broker.DefaultPingInterval,
		"idle keep-alive period for event-stream responses; negative disables ping injection")
	f.String("agent-namespace", "patchy-agents", "namespace whose agent service account callers must be")
	f.String("agent-service-account", "patchy-agent", "the only service account the broker answers to")
	f.Int("max-request-bytes", broker.DefaultMaxRequestBytes,
		"largest request body a payload-signing route (bedrock) accepts")

	// A route exists iff its identifying flag is set; at least one must be.
	f.String("anthropic-api-key-file", "",
		"mounted Secret file holding the Anthropic credential; set to enable the anthropic route")
	f.String("anthropic-auth", "key",
		"anthropic credential mode: key (x-api-key) or token (Authorization bearer — a claude setup-token OAuth token)")
	f.String("anthropic-base-url", "https://api.anthropic.com", "Anthropic API base URL")
	f.String("bedrock-region", "", "AWS region for Bedrock SigV4 signing; set to enable the bedrock route")
	f.String("bedrock-base-url", "",
		"Bedrock runtime base URL (default: https://bedrock-runtime.<bedrock-region>.amazonaws.com)")
	f.String("vertex-region", "", "GCP region for Vertex AI; set to enable the vertex route")
	f.String("vertex-base-url", "",
		"Vertex AI base URL (default: https://<vertex-region>-aiplatform.googleapis.com)")
	f.String("foundry-resource", "", "Microsoft Foundry resource name; set to enable the foundry route")
	f.String("foundry-base-url", "",
		"Foundry base URL (default: https://<foundry-resource>.services.ai.azure.com)")
	f.String("foundry-auth", "key", "foundry credential mode: key (x-api-key from file) or entra (bearer)")
	f.String("foundry-api-key-file", "", "mounted Secret file holding the Foundry API key (foundry-auth key)")

	// The codex routes: OpenAI's Responses API, first-party or on Azure.
	f.String("openai-api-key-file", "",
		"mounted Secret file holding the OpenAI platform API key; set to enable the openai route")
	f.String("openai-base-url", "https://api.openai.com", "OpenAI API base URL (codex appends /v1)")
	f.String("azure-resource", "", "Azure OpenAI resource name; set to enable the azure route")
	f.String("azure-base-url", "",
		"Azure OpenAI base URL, codex appends /v1 (default: https://<azure-resource>.openai.azure.com/openai)")
	f.String("azure-auth", "key", "azure credential mode: key (api-key from file) or entra (bearer)")
	f.String("azure-api-key-file", "", "mounted Secret file holding the Azure OpenAI API key (azure-auth key)")
	return cmd
}

// upstreams builds the route table from the flags: one route per provider
// whose identifying flag is set, at least one required.
func upstreams(ctx context.Context, opts *cli.Options) (map[string]broker.Upstream, error) {
	routes := map[string]broker.Upstream{}
	builders := []struct {
		name  string
		build func(context.Context, *cli.Options) (*broker.Upstream, error)
	}{
		{"anthropic", anthropicUpstream},
		{"bedrock", bedrockUpstream},
		{"vertex", vertexUpstream},
		{"foundry", foundryUpstream},
		{"openai", openAIUpstream},
		{"azure", azureUpstream},
	}
	for _, b := range builders {
		up, err := b.build(ctx, opts)
		if err != nil {
			return nil, err
		}
		if up != nil {
			routes[b.name] = *up
		}
	}
	if len(routes) == 0 {
		return nil, errors.New("no upstream configured (set at least one of --anthropic-api-key-file / " +
			"--bedrock-region / --vertex-region / --foundry-resource / --openai-api-key-file / --azure-resource)")
	}
	return routes, nil
}

func anthropicUpstream(_ context.Context, opts *cli.Options) (*broker.Upstream, error) {
	keyFile := opts.String("anthropic-api-key-file")
	if keyFile == "" {
		return nil, nil
	}
	target, err := broker.ParseTarget(opts.String("anthropic-base-url"))
	if err != nil {
		return nil, err
	}
	var bearer bool
	switch mode := opts.String("anthropic-auth"); mode {
	case "key":
	case "token":
		bearer = true
	default:
		return nil, fmt.Errorf("--anthropic-auth %q is not key or token", mode)
	}
	up := broker.Anthropic(target, keyFile, bearer)
	return &up, nil
}

func bedrockUpstream(ctx context.Context, opts *cli.Options) (*broker.Upstream, error) {
	region := opts.String("bedrock-region")
	if region == "" {
		return nil, nil
	}
	raw := opts.String("bedrock-base-url")
	if raw == "" {
		raw = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", region)
	}
	target, err := broker.ParseTarget(raw)
	if err != nil {
		return nil, err
	}
	up, err := broker.Bedrock(ctx, target, region)
	if err != nil {
		return nil, err
	}
	return &up, nil
}

func vertexUpstream(ctx context.Context, opts *cli.Options) (*broker.Upstream, error) {
	region := opts.String("vertex-region")
	if region == "" {
		return nil, nil
	}
	raw := opts.String("vertex-base-url")
	if raw == "" {
		raw = fmt.Sprintf("https://%s-aiplatform.googleapis.com", region)
	}
	target, err := broker.ParseTarget(raw)
	if err != nil {
		return nil, err
	}
	up, err := broker.Vertex(ctx, target)
	if err != nil {
		return nil, err
	}
	return &up, nil
}

func foundryUpstream(_ context.Context, opts *cli.Options) (*broker.Upstream, error) {
	return keyOrEntraUpstream(opts, "foundry", "https://%s.services.ai.azure.com",
		broker.FoundryKey, broker.FoundryEntra)
}

func openAIUpstream(_ context.Context, opts *cli.Options) (*broker.Upstream, error) {
	keyFile := opts.String("openai-api-key-file")
	if keyFile == "" {
		return nil, nil
	}
	target, err := broker.ParseTarget(opts.String("openai-base-url"))
	if err != nil {
		return nil, err
	}
	up := broker.OpenAI(target, keyFile)
	return &up, nil
}

func azureUpstream(_ context.Context, opts *cli.Options) (*broker.Upstream, error) {
	return keyOrEntraUpstream(opts, "azure", "https://%s.openai.azure.com/openai",
		broker.AzureOpenAIKey, broker.AzureOpenAIEntra)
}

// keyOrEntraUpstream builds an Azure-hosted route (foundry, azure) from its
// --<name>-resource / --<name>-base-url / --<name>-auth /
// --<name>-api-key-file flags: disabled when neither the resource nor the
// base URL is set; the base URL defaults from the resource via defaultURL;
// the credential is a mounted key file (key) or the broker's ambient Entra
// identity (entra).
func keyOrEntraUpstream(opts *cli.Options, name, defaultURL string,
	key func(*url.URL, string) broker.Upstream,
	entra func(*url.URL) (broker.Upstream, error)) (*broker.Upstream, error) {
	resource, raw := opts.String(name+"-resource"), opts.String(name+"-base-url")
	if resource == "" && raw == "" {
		return nil, nil
	}
	if raw == "" {
		raw = fmt.Sprintf(defaultURL, resource)
	}
	target, err := broker.ParseTarget(raw)
	if err != nil {
		return nil, err
	}
	switch mode := opts.String(name + "-auth"); mode {
	case "key":
		keyFile := opts.String(name + "-api-key-file")
		if keyFile == "" {
			return nil, fmt.Errorf("--%s-auth key requires --%s-api-key-file", name, name)
		}
		up := key(target, keyFile)
		return &up, nil
	case "entra":
		up, err := entra(target)
		if err != nil {
			return nil, err
		}
		return &up, nil
	default:
		return nil, fmt.Errorf("--%s-auth %q is not key or entra", name, mode)
	}
}

func serve(ctx context.Context, opts *cli.Options) error {
	prov, shutdown, err := telemetry.Init(ctx, telemetry.Config{
		Dir:            os.Getenv("PATCHY_TELEMETRY_DIR"),
		Level:          opts.LogLevel,
		ServiceName:    "egress-broker",
		ServiceVersion: version.Version,
	})
	if err != nil {
		prov.Logger.LogAttrs(ctx, slog.LevelWarn, "telemetry disabled", slog.Any("error", err))
	}
	defer func() { _ = shutdown(context.WithoutCancel(ctx)) }()
	log := prov.Logger

	restCfg, err := kube.RestConfig(opts.String("kubeconfig"))
	if err != nil {
		return fmt.Errorf("kubernetes config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("kubernetes clientset: %w", err)
	}

	routes, err := upstreams(ctx, opts)
	if err != nil {
		return err
	}
	srv, err := broker.New(cs, broker.Config{
		Audience:            opts.String("token-audience"),
		AgentNamespace:      opts.String("agent-namespace"),
		AgentServiceAccount: opts.String("agent-service-account"),
		VerdictTTL:          opts.Duration("verdict-ttl"),
		PingInterval:        opts.Duration("sse-ping-interval"),
		MaxRequestBytes:     int64(opts.Int("max-request-bytes")),
		Upstreams:           routes,
	}, log)
	if err != nil {
		return err
	}

	// The proxy listener has no write or handler deadline on purpose:
	// evaluation runs stream for hours. ReadHeaderTimeout still bounds a
	// stuck caller.
	proxySrv := &http.Server{
		Addr:              opts.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	healthSrv := &http.Server{
		Addr:              opts.String("health-addr"),
		Handler:           srv.HealthHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	names := slices.Sorted(maps.Keys(routes))
	log.LogAttrs(ctx, slog.LevelInfo, "egress-broker starting",
		slog.String("listen_addr", proxySrv.Addr),
		slog.String("health_addr", healthSrv.Addr),
		slog.Any("routes", names))

	g, ctx := errgroup.WithContext(ctx)
	for _, s := range []*http.Server{proxySrv, healthSrv} {
		g.Go(func() error {
			if err := s.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})
		g.Go(func() error {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			return s.Shutdown(shutdownCtx)
		})
	}
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
