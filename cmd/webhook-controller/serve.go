// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
	"github.com/bitwise-media-group/patchy/internal/version"
	"github.com/bitwise-media-group/patchy/internal/webhook"
	"github.com/bitwise-media-group/patchy/internal/webhookctrl"
)

func newServeCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the fan-out webhook receiver",
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), opts) },
	}
	cmd.Flags().String("forward-routes", "",
		"comma-separated event=url routes, e.g. issues=http://ctx:8080/webhook; '*' catches unrouted events (required)")
	cmd.Flags().Duration("forward-timeout", 10*time.Second, "per-target forward timeout")
	return cmd
}

func serve(ctx context.Context, opts *cli.Options) error {
	prov, shutdown, err := telemetry.Init(ctx, telemetry.Config{
		Dir:            os.Getenv("PATCHY_TELEMETRY_DIR"),
		Level:          opts.LogLevel,
		ServiceName:    "webhook-controller",
		ServiceVersion: version.Version,
	})
	if err != nil {
		prov.Logger.LogAttrs(ctx, slog.LevelWarn, "telemetry disabled", slog.Any("error", err))
	}
	defer func() { _ = shutdown(context.WithoutCancel(ctx)) }()
	log := prov.Logger

	secret, err := opts.WebhookSecret()
	if err != nil {
		return err
	}
	routes, err := parseRoutes(opts.String("forward-routes"))
	if err != nil {
		return err
	}

	fwd := webhookctrl.New(webhookctrl.Config{
		Secret:  secret,
		Routes:  routes,
		Timeout: opts.Duration("forward-timeout"),
	}, log)
	srv := webhook.NewServer(webhook.Config{Addr: opts.ListenAddr, Secret: secret}, log, fwd)

	log.LogAttrs(ctx, slog.LevelInfo, "webhook-controller starting",
		slog.String("addr", opts.ListenAddr),
		slog.Any("routes", routes))

	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// parseRoutes splits and validates the comma-separated event=url route list.
// The same event may appear more than once (it then fans out to every listed
// target), and the webhookctrl.DefaultRoute key ("*") catches event types
// with no route of their own. Every URL must be absolute http(s).
func parseRoutes(raw string) (map[string][]string, error) {
	routes := make(map[string][]string)
	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		event, target, ok := strings.Cut(entry, "=")
		event, target = strings.TrimSpace(event), strings.TrimSpace(target)
		if !ok || event == "" || target == "" {
			return nil, fmt.Errorf("forward route %q is not event=url", entry)
		}
		u, err := url.Parse(target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("forward route %q: %q is not an absolute http(s) URL", entry, target)
		}
		routes[event] = append(routes[event], target)
	}
	if len(routes) == 0 {
		return nil, errors.New("--forward-routes (or PATCHY_FORWARD_ROUTES) must name at least one event=url route")
	}
	return routes, nil
}
