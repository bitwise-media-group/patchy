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

	"github.com/bitwise-media-group/patchy/internal/archive"
	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/ghuser"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/repoaccess"
	"github.com/bitwise-media-group/patchy/internal/telemetry"
	"github.com/bitwise-media-group/patchy/internal/version"
	"github.com/bitwise-media-group/patchy/internal/web"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
	"github.com/bitwise-media-group/patchy/internal/web/ghlink"
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
		"path to the mounted authentication config; absent serves nothing (set mode none to disable authentication)")
	archive.RegisterFlags(f)
	f.Duration("archive-presign-ttl", web.DefaultPresignTTL,
		"how long a presigned archive URL handed to the browser stays valid")
	f.String("integration-controller-url", "http://patchy-integration-controller.patchy.svc.cluster.local:9792",
		"internal URL of the integration-controller's repository-access endpoint, which tells the repositories "+
			"page whether patchy can read a repository's alerts (empty disables the check)")
	f.Duration("repository-backfill-cooldown", web.DefaultBackfillCooldown,
		"how long after a repository's backfill ran that the repositories page refuses another for it "+
			"(the integration-controller's --backfill-request-ttl must outlast it)")
	f.String("integration-controller-token-file", "/var/run/secrets/patchy/integration-controller/token",
		"projected ServiceAccount token (audience "+repoaccess.Audience+") sent to the integration-controller; "+
			"read again on every call")
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

// archiveAccess opens the run archive the server presigns for, or returns a
// nil store when archival is not configured.
func archiveAccess(ctx context.Context, opts *cli.Options, log *slog.Logger) (archive.Store, time.Duration, error) {
	cfg := archive.ConfigFrom(opts)
	if !cfg.Enabled() {
		return nil, 0, nil
	}
	ttl := opts.Duration("archive-presign-ttl")
	if ttl <= 0 || ttl > archive.MaxPresignTTL {
		return nil, 0, fmt.Errorf("--archive-presign-ttl %s must be in (0, %s]", ttl, archive.MaxPresignTTL)
	}
	store, err := archive.Open(ctx, cfg)
	if err != nil {
		return nil, 0, err
	}
	log.LogAttrs(ctx, slog.LevelInfo, "run archive access enabled",
		slog.String("provider", string(cfg.Provider)), slog.String("bucket", cfg.Bucket),
		slog.Duration("presign_ttl", ttl))
	return store, ttl, nil
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
			"authentication not configured, so serving nothing: findings, metrics and actions are unavailable "+
				"until an auth config is mounted")
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

	// The finding-detail projection looks child runs up by owning finding;
	// the index must exist before the cache starts serving.
	if err := web.RegisterIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
		return err
	}

	// Mode none bypasses access reviews entirely; every other posture asks
	// the cluster per identity.
	var granter web.Granter = authz.NewReviewer(mgr.GetClient(), namespace, 0)
	if authCfg != nil && authCfg.Mode == auth.ModeNone {
		granter = authz.Full{}
	}

	srv := web.NewServer(mgr.GetClient(), namespace, authn, granter, log)
	// Transcripts read through the uncached API reader, because caching every
	// ConfigMap in the namespace for objects read once per page view is not
	// worth an informer. Live runs are followed through the agent namespace's
	// pod logs via the API server. The status server never dials an agent pod.
	agentNS := opts.String("agent-namespace")
	tailer, err := agentTailer(opts.String("kubeconfig"), agentNS)
	if err != nil {
		// Live following is a nicety; persisted transcripts still serve.
		log.LogAttrs(ctx, slog.LevelWarn, "live transcripts unavailable",
			slog.String("agent_namespace", agentNS), slog.Any("error", err))
	}
	srv = srv.WithTranscripts(mgr.GetAPIReader(), tailer)

	// Archived runs are read by the browser straight from the bucket through
	// short-lived presigned URLs; this process only signs them.
	store, ttl, err := archiveAccess(ctx, opts, log)
	if err != nil {
		return err
	}
	if store != nil {
		defer func() { _ = store.Close() }()
		srv = srv.WithArchive(store, ttl)
	}

	// The repositories page asks the integration-controller, which holds the
	// forge credentials, whether a repository's alerts are readable; this
	// process stays credential-free.
	srv = srv.WithBackfillCooldown(opts.Duration("repository-backfill-cooldown"))
	if srv, err = withIntegrationController(ctx, opts, srv, authCfg, authn, log); err != nil {
		return err
	}

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

// withIntegrationController wires the integration-controller's internal
// endpoints: the repository-access check, and the optional "Connect GitHub"
// link. That process holds the forge credentials, so this one stays
// credential-free. Every GitHub call goes through it. The link cookies are
// sealed with a key from the auth config; without one (mode anonymous or
// none and no linkKeyFile) linking stays off.
func withIntegrationController(
	ctx context.Context, opts *cli.Options, srv *web.Server, authCfg *auth.Config, authn auth.Authenticator,
	log *slog.Logger,
) (*web.Server, error) {
	url := opts.String("integration-controller-url")
	if url == "" {
		return srv, nil
	}
	tokenFile := opts.String("integration-controller-token-file")
	srv = srv.WithRepositoryAccess(&repoaccess.Client{URL: url, TokenFile: tokenFile})
	log.LogAttrs(ctx, slog.LevelInfo, "repository access check enabled", slog.String("url", url))

	linkKey, err := auth.LinkKey(authCfg)
	if err != nil || linkKey == nil {
		return srv, err
	}
	users := &ghuser.Client{URL: url, TokenFile: tokenFile}
	links := ghlink.New(ghlink.Config{
		Key:      linkKey,
		Secure:   !authCfg.Insecure,
		MaxAge:   authCfg.SessionDuration.Duration,
		BaseURL:  func(r *http.Request) string { return auth.BaseURL(authCfg, r) },
		Lookup:   srv.LookupConnectable,
		Identify: authn.Identify,
		Users:    users,
		Log:      log,
	})
	log.LogAttrs(ctx, slog.LevelInfo, "github account linking enabled")
	return srv.WithGitHub(users, links), nil
}
