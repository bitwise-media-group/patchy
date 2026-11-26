// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/forge"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/integrationcap"
	"github.com/bitwise-media-group/patchy/internal/repoaccess"
	"github.com/bitwise-media-group/patchy/internal/tokenauth"
)

// Repository-access cache bounds. A result is cached for AccessTTL whether it
// is good or bad, so a page left open does not turn into GitHub API traffic;
// a transient failure (AccessError) is cached only briefly.
const (
	// AccessTTL is how long a probe result is reused.
	AccessTTL = 5 * time.Minute
	// accessErrorTTL is how long a transient failure is reused.
	accessErrorTTL = 30 * time.Second
	// accessCacheSize bounds the cache: one entry per (Integration, repo)
	// and one per repo for the Forge check.
	accessCacheSize = 2048
	// accessParallelism bounds how many repositories are probed at once.
	accessParallelism = 4
	// maxAccessBody caps the request body (20 short names).
	maxAccessBody = 16 << 10
)

// callerAuth authenticates the endpoint's caller; tokenauth.Authenticator
// implements it.
type callerAuth interface {
	Authenticate(r *http.Request, header string) (tokenauth.Identity, error)
}

// forgeCoverage resolves a repository's Forge and its contents permission;
// forge.Store implements it.
type forgeCoverage interface {
	Resolve(ctx context.Context, namespace, repoURL string) (*forge.Resolved, error)
	ContentsPermission(ctx context.Context, res *forge.Resolved) (string, error)
}

// alertClient is the slice of a GitHub client the probe needs.
type alertClient interface {
	ProbeAlerts(ctx context.Context, repo ghclient.Repo, kind ghclient.AlertKind) (ghclient.AccessStatus, string)
}

// RepositoryAccess serves the internal repository-access endpoint
// (internal/repoaccess): for each requested repository, whether each
// eligible github Integration can read its alerts, and whether a Forge can
// remediate it. Only the status server calls it. It never writes anything,
// and its results live only in a small in-memory cache.
type RepositoryAccess struct {
	// Reader lists Integrations (the manager's cache).
	Reader client.Reader
	// Creds builds the Integration GitHub clients.
	Creds *Creds
	// Forges resolves Forge coverage; nil skips the Forge check.
	Forges forgeCoverage
	// Auth admits only the status server's ServiceAccount.
	Auth      callerAuth
	Namespace string
	Log       *slog.Logger

	// clientFor overrides Creds.Client (tests).
	clientFor func(ctx context.Context, integ *v1alpha1.Integration, repo ghclient.Repo) (alertClient, error)
	now       func() time.Time

	once  sync.Once
	cache *accessCache
}

// Handler is the internal listener's surface: the one endpoint.
func (a *RepositoryAccess) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+repoaccess.Path, a.serve)
	return mux
}

func (a *RepositoryAccess) log() *slog.Logger {
	if a.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return a.Log
}

func (a *RepositoryAccess) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *RepositoryAccess) serve(w http.ResponseWriter, r *http.Request) {
	id, err := a.Auth.Authenticate(r, repoaccess.TokenHeader)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req repoaccess.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAccessBody)).Decode(&req); err != nil {
		http.Error(w, "malformed request body", http.StatusBadRequest)
		return
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	results, err := a.Check(r.Context(), req.Repositories)
	if err != nil {
		a.log().LogAttrs(r.Context(), slog.LevelError, "repository access check failed", slog.Any("error", err))
		http.Error(w, "access check failed", http.StatusInternalServerError)
		return
	}
	a.log().LogAttrs(r.Context(), slog.LevelDebug, "repository access checked",
		slog.String("caller", id.User), slog.Int("repositories", len(req.Repositories)))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(repoaccess.Response{Results: results})
}

// Check probes each repository, in order, against every eligible
// Integration and the Forge store.
func (a *RepositoryAccess) Check(ctx context.Context, repos []string) ([]repoaccess.Result, error) {
	a.once.Do(func() { a.cache = newAccessCache(accessCacheSize) })

	var all v1alpha1.IntegrationList
	if err := a.Reader.List(ctx, &all, client.InNamespace(a.Namespace)); err != nil {
		return nil, fmt.Errorf("list integrations: %w", err)
	}
	var integs []*v1alpha1.Integration
	for i := range all.Items {
		if eligibleForAccess(&all.Items[i]) {
			integs = append(integs, &all.Items[i])
		}
	}
	sort.Slice(integs, func(i, j int) bool { return integs[i].Name < integs[j].Name })
	// Repositories are named "owner/name" like the findings filter; the host
	// is the first eligible Integration's (github.com unless it is GHES).
	host := "github.com"
	if len(integs) > 0 {
		host = githubHost(integs[0])
	}

	out := make([]repoaccess.Result, len(repos))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(accessParallelism)
	for i, name := range repos {
		g.Go(func() error {
			owner, repoName, _ := strings.Cut(name, "/")
			repo := ghclient.Repo{Owner: owner, Name: repoName}
			res := repoaccess.Result{Repo: name, Integrations: []repoaccess.IntegrationAccess{}}
			for _, integ := range integs {
				res.Integrations = append(res.Integrations, a.integrationAccess(gctx, integ, repo))
			}
			res.Forge = a.forgeAccess(gctx, host, repo)
			out[i] = res
			return nil
		})
	}
	_ = g.Wait()
	return out, nil
}

// IntegrationAccess probes one Integration's alert access to one
// repository, through the shared cache.
func (a *RepositoryAccess) IntegrationAccess(
	ctx context.Context, integ *v1alpha1.Integration, repo ghclient.Repo,
) repoaccess.IntegrationAccess {
	a.once.Do(func() { a.cache = newAccessCache(accessCacheSize) })
	return a.integrationAccess(ctx, integ, repo)
}

// eligibleForAccess is the Integration filter: github, not suspended, and
// ingesting an alert family a backfill could list.
func eligibleForAccess(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && integrationcap.BackfillSupported(i)
}

// integrationAccess probes one Integration's view of one repository,
// through the cache. The key carries the Integration's generation, so a spec
// change (a capability switched on, a new credential reference) is seen at
// once.
func (a *RepositoryAccess) integrationAccess(
	ctx context.Context, integ *v1alpha1.Integration, repo ghclient.Repo,
) repoaccess.IntegrationAccess {
	key := "i|" + integ.Name + "|" + strconv.FormatInt(integ.Generation, 10) + "|" + repo.String()
	if v, ok := a.cache.get(key, a.clock()); ok {
		return v.(repoaccess.IntegrationAccess)
	}
	out := repoaccess.IntegrationAccess{Name: integ.Name}
	code, secrets := codeScanningEnabled(integ), secretScanningEnabled(integ)
	c, err := a.client(ctx, integ, repo)
	if err != nil {
		// For an App, building the client resolves the installation, so a
		// 404 here is "not installed"; the same outcome holds for both
		// families.
		st, detail := ghclient.ClassifyAccess(err)
		check := &repoaccess.Check{Status: repoaccess.Status(st), Detail: detail}
		if code {
			out.Alerts.CodeScanning = check
		}
		if secrets {
			out.Alerts.SecretScanning = check
		}
	} else {
		if code {
			st, detail := c.ProbeAlerts(ctx, repo, ghclient.AlertKindCodeScanning)
			out.Alerts.CodeScanning = &repoaccess.Check{Status: repoaccess.Status(st), Detail: detail}
		}
		if secrets {
			st, detail := c.ProbeAlerts(ctx, repo, ghclient.AlertKindSecretScanning)
			out.Alerts.SecretScanning = &repoaccess.Check{Status: repoaccess.Status(st), Detail: detail}
		}
	}
	ttl := AccessTTL
	if transient(out.Alerts.CodeScanning) || transient(out.Alerts.SecretScanning) {
		ttl = accessErrorTTL
	}
	if ctx.Err() == nil {
		a.cache.put(key, out, a.clock().Add(ttl))
	}
	return out
}

func transient(c *repoaccess.Check) bool {
	return c != nil && c.Status == repoaccess.StatusError
}

func (a *RepositoryAccess) client(
	ctx context.Context, integ *v1alpha1.Integration, repo ghclient.Repo,
) (alertClient, error) {
	if a.clientFor != nil {
		return a.clientFor(ctx, integ, repo)
	}
	return a.Creds.Client(ctx, integ, repo)
}

// forgeAccess resolves the Forge covering the repository and, for an App
// credential, its contents permission. Forge gaps never block a backfill;
// the status page shows them as a separate warning.
func (a *RepositoryAccess) forgeAccess(ctx context.Context, host string, repo ghclient.Repo) *repoaccess.ForgeAccess {
	if a.Forges == nil {
		return nil
	}
	key := "f|" + host + "|" + repo.String()
	if v, ok := a.cache.get(key, a.clock()); ok {
		fa := v.(repoaccess.ForgeAccess)
		return &fa
	}
	fa := a.resolveForge(ctx, host, repo)
	ttl := AccessTTL
	if fa.Status == repoaccess.ForgeError {
		ttl = accessErrorTTL
	}
	if ctx.Err() == nil {
		a.cache.put(key, fa, a.clock().Add(ttl))
	}
	return &fa
}

func (a *RepositoryAccess) resolveForge(ctx context.Context, host string, repo ghclient.Repo) repoaccess.ForgeAccess {
	res, err := a.Forges.Resolve(ctx, a.Namespace, "https://"+host+"/"+repo.String())
	switch {
	case errors.Is(err, forge.ErrNoMatch):
		return repoaccess.ForgeAccess{Status: repoaccess.ForgeMissing, Detail: "no Forge covers " + repo.String()}
	case errors.Is(err, forge.ErrAmbiguous):
		return repoaccess.ForgeAccess{Status: repoaccess.ForgeAmbiguous, Detail: err.Error()}
	case err != nil:
		return repoaccess.ForgeAccess{Status: repoaccess.ForgeError, Detail: err.Error()}
	}
	out := repoaccess.ForgeAccess{Name: res.Forge.Name}
	contents, err := a.Forges.ContentsPermission(ctx, res)
	if err != nil {
		_, detail := ghclient.ClassifyAccess(err)
		out.Status, out.Detail = repoaccess.ForgeError, detail
		return out
	}
	out.Contents = contents
	switch contents {
	case repoaccess.ContentsWrite, repoaccess.ContentsUnknown:
		out.Status = repoaccess.ForgeOK
	default:
		out.Status = repoaccess.ForgeNoWrite
	}
	return out
}

// accessCache is a small LRU with per-entry expiry.
type accessCache struct {
	mu    sync.Mutex
	size  int
	order *list.List // front = most recent; values are *accessEntry
	items map[string]*list.Element
}

type accessEntry struct {
	key    string
	value  any
	expiry time.Time
}

func newAccessCache(size int) *accessCache {
	return &accessCache{size: size, order: list.New(), items: map[string]*list.Element{}}
}

func (c *accessCache) get(key string, now time.Time) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*accessEntry)
	if now.After(e.expiry) {
		c.order.Remove(el)
		delete(c.items, key)
		return nil, false
	}
	c.order.MoveToFront(el)
	return e.value, true
}

func (c *accessCache) put(key string, value any, expiry time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value = &accessEntry{key: key, value: value, expiry: expiry}
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&accessEntry{key: key, value: value, expiry: expiry})
	for c.order.Len() > c.size {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*accessEntry).key)
	}
}
