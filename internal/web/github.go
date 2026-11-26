// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghuser"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/internal/web/ghlink"
)

// The optional "Connect GitHub" link (internal/web/ghlink): a signed-in
// user's GitHub identity per github Integration. With it the repositories
// page lists what the user maintains, and the scoped maintainer verbs act on
// those repositories.

const (
	// maintainedTTL is how long a user's maintained-repository set is reused
	// to decide which buttons show. The POST always re-checks live.
	maintainedTTL = 5 * time.Minute
	// maintainedCacheLimit bounds the cache; at the limit it resets, like
	// the grant cache.
	maintainedCacheLimit = 1024
)

// githubUsers is the slice of the integration-controller client the
// maintainer paths call; ghuser.Client implements it.
type githubUsers interface {
	Repositories(ctx context.Context, req ghuser.RepositoriesRequest) (*ghuser.RepositoriesResponse, error)
	AuthorizeRepository(ctx context.Context, req ghuser.AuthorizeRepositoryRequest) (
		*ghuser.AuthorizeRepositoryResponse, error)
}

// WithGitHub wires the GitHub link: the integration-controller's user
// endpoints and the linker over the link cookies. Either nil leaves the
// feature off.
func (s *Server) WithGitHub(users githubUsers, links *ghlink.Linker) *Server {
	if users == nil || links == nil {
		return s
	}
	s.ghUsers, s.links = users, links
	return s
}

// githubEnabled reports whether GitHub linking is wired.
func (s *Server) githubEnabled() bool { return s.ghUsers != nil && s.links != nil }

// GitHubLink is one connectable Integration as the user menu and the
// repositories page see it. It mirrors ui/src/types.ts.
type GitHubLink struct {
	Integration string `json:"integration"`
	Host        string `json:"host"`
	Login       string `json:"login,omitempty"`
	Connected   bool   `json:"connected"`
}

// connectable lists the github Integrations a user may link: user
// authorization configured and ready, not suspended. Sorted by name.
func (s *Server) connectable(ctx context.Context) []ghlink.Integration {
	if !s.githubEnabled() {
		return nil
	}
	var integs v1alpha1.IntegrationList
	if err := s.client.List(ctx, &integs, client.InNamespace(s.namespace)); err != nil {
		s.log.LogAttrs(ctx, slog.LevelWarn, "list integrations for github links", slog.Any("error", err))
		return nil
	}
	var out []ghlink.Integration
	for i := range integs.Items {
		if gi, ok := connectableIntegration(&integs.Items[i]); ok {
			out = append(out, gi)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func connectableIntegration(integ *v1alpha1.Integration) (ghlink.Integration, bool) {
	gh := integ.Spec.GitHub
	if integ.Spec.Provider != v1alpha1.IntegrationProviderGitHub || gh == nil || gh.UserAuthorization == nil ||
		integ.Spec.Suspend {
		return ghlink.Integration{}, false
	}
	if !meta.IsStatusConditionTrue(integ.Status.Conditions, v1alpha1.ConditionUserAuthorizationReady) {
		return ghlink.Integration{}, false
	}
	return ghlink.Integration{Name: integ.Name, BaseURL: gh.BaseURL, ClientID: gh.UserAuthorization.ClientID}, true
}

// LookupConnectable resolves one connectable Integration by name; the
// linker's Lookup.
func (s *Server) LookupConnectable(ctx context.Context, name string) (ghlink.Integration, bool) {
	var integ v1alpha1.Integration
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &integ); err != nil {
		return ghlink.Integration{}, false
	}
	return connectableIntegration(&integ)
}

// linked is one live link with the Integration it belongs to.
type linked struct {
	integ ghlink.Integration
	link  *ghlink.Link
}

// userLinks reads the caller's links over every connectable Integration, in
// name order, and the user-menu view of them.
func (s *Server) userLinks(r *http.Request, id auth.Identity) ([]linked, []GitHubLink) {
	integs := s.connectable(r.Context())
	if len(integs) == 0 {
		return nil, nil
	}
	var live []linked
	view := make([]GitHubLink, 0, len(integs))
	for _, gi := range integs {
		gl := GitHubLink{Integration: gi.Name, Host: gi.Host()}
		if link, ok := s.links.Get(r, id, gi.Name); ok {
			gl.Login, gl.Connected = link.Login, true
			live = append(live, linked{integ: gi, link: link})
		}
		view = append(view, gl)
	}
	return live, view
}

// repoHost is the lower-case host of a finding's repository URL.
func repoHost(repoURL string) string {
	u, err := url.Parse(repoURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// linkForFinding chooses the link a maintainer action on f acts through:
// the finding's own Integration when the user linked it, else any linked
// Integration on the repository's host. ok is false for a finding with no
// github repository, or no fitting link.
func linkForFinding(f *v1alpha1.Finding, live []linked) (linked, string, bool) {
	repo := f.Spec.Repository
	if repo == nil || repo.Type != v1alpha1.RepositoryTypeGitHub || repo.Name == "" {
		return linked{}, "", false
	}
	for _, l := range live {
		if l.integ.Name == f.Spec.IntegrationRef.Name {
			return l, repo.Name, true
		}
	}
	host := repoHost(repo.URL)
	for _, l := range live {
		if host != "" && l.integ.Host() == host {
			return l, repo.Name, true
		}
	}
	return linked{}, repo.Name, false
}

// maintainedCache holds each (Integration, GitHub user) pair's maintained
// repositories for maintainedTTL.
type maintainedCache struct {
	mu      sync.Mutex
	items   map[string]maintainedEntry
	filling map[string]bool
}

type maintainedEntry struct {
	repos   map[string]string // repo → role
	expires time.Time
}

func maintainedKey(integration string, userID int64) string {
	return integration + "\x00" + strconv.FormatInt(userID, 10)
}

func (c *maintainedCache) get(key string, now time.Time) (map[string]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok || now.After(e.expires) {
		return nil, false
	}
	return e.repos, true
}

func (c *maintainedCache) put(key string, repos []ghuser.Repository, now time.Time) {
	set := make(map[string]string, len(repos))
	for _, r := range repos {
		set[strings.ToLower(r.Name)] = r.Role
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil || len(c.items) >= maintainedCacheLimit {
		c.items = map[string]maintainedEntry{}
	}
	c.items[key] = maintainedEntry{repos: set, expires: now.Add(maintainedTTL)}
}

// startFill claims a background fill for key; false when one runs already.
func (c *maintainedCache) startFill(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.filling == nil {
		c.filling = map[string]bool{}
	}
	if c.filling[key] {
		return false
	}
	c.filling[key] = true
	return true
}

func (c *maintainedCache) endFill(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.filling, key)
}

// maintainedSets returns, for each live link whose set is cached, the
// user's maintained repositories. A miss starts a background fill (never
// waited on): its completion signals the SSE refetch, and the next page
// load shows the scoped buttons.
func (s *Server) maintainedSets(ctx context.Context, live []linked) map[string]map[string]string {
	out := map[string]map[string]string{}
	now := s.now()
	for _, l := range live {
		key := maintainedKey(l.integ.Name, l.link.UserID)
		if set, ok := s.maintained.get(key, now); ok {
			out[l.integ.Name] = set
			continue
		}
		// A background fill cannot rewrite the cookie, so it runs only on a
		// token that is still live; an expiring one waits for a request that
		// can refresh it.
		if !l.link.Expiry.IsZero() && now.Add(time.Minute).After(l.link.Expiry) {
			continue
		}
		if !s.maintained.startFill(key) {
			continue
		}
		go s.fillMaintained(context.WithoutCancel(ctx), key, l.integ.Name, l.link.AccessToken)
	}
	return out
}

func (s *Server) fillMaintained(ctx context.Context, key, integration, token string) {
	defer s.maintained.endFill(key)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := s.ghUsers.Repositories(ctx, ghuser.RepositoriesRequest{Integration: integration, AccessToken: token})
	if err != nil {
		s.log.LogAttrs(ctx, slog.LevelInfo, "maintained repositories fill failed",
			slog.String("integration", integration), slog.Any("error", err))
		return
	}
	s.maintained.put(key, res.Repositories, s.now())
	s.broker.publish(eventFindingsChanged)
}

// findingVerbs returns the verbs the caller may use on f: the namespace
// grants, plus each scoped maintainer verb (as its base verb) when f's
// repository is in the user's cached maintained set.
func findingVerbs(
	f *v1alpha1.Finding, base []string, scoped []string, live []linked, sets map[string]map[string]string,
) []string {
	if len(scoped) == 0 {
		return base
	}
	l, repo, ok := linkForFinding(f, live)
	if !ok {
		return base
	}
	set := sets[l.integ.Name]
	if _, maintains := set[strings.ToLower(repo)]; !maintains {
		return base
	}
	out := append([]string(nil), base...)
	for _, v := range scoped {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// GitHubRepositoriesPayload is the payload behind GET /api/github/repositories.
type GitHubRepositoriesPayload struct {
	Integration  string              `json:"integration"`
	Login        string              `json:"login"`
	Repositories []ghuser.Repository `json:"repositories"`
	Truncated    bool                `json:"truncated,omitempty"`
}

// handleGitHubRepositories serves GET /api/github/repositories?integration=
// behind the view gate: the repositories the caller maintains through that
// Integration's App, for the import picker. It fills the maintained cache.
func (s *Server) handleGitHubRepositories(w http.ResponseWriter, r *http.Request) {
	v, ok := s.authorize(w, r)
	if !ok {
		return
	}
	if !s.githubEnabled() {
		http.Error(w, "GitHub linking is not configured.", http.StatusNotFound)
		return
	}
	name := r.URL.Query().Get("integration")
	gi, ok := s.LookupConnectable(r.Context(), name)
	if !ok {
		http.Error(w, "Integration "+name+" cannot connect GitHub accounts.", http.StatusNotFound)
		return
	}
	link, ok := s.links.Get(r, v.id, gi.Name)
	if !ok {
		http.Error(w, "Connect GitHub on "+gi.Host()+" first.", http.StatusConflict)
		return
	}
	var res *ghuser.RepositoriesResponse
	err := s.links.Call(r.Context(), w, link, func(token string) error {
		var err error
		res, err = s.ghUsers.Repositories(r.Context(), ghuser.RepositoriesRequest{Integration: gi.Name, AccessToken: token})
		return err
	})
	if err != nil {
		s.writeGitHubError(w, r, gi, err)
		return
	}
	s.maintained.put(maintainedKey(gi.Name, link.UserID), res.Repositories, s.now())
	writeJSON(w, GitHubRepositoriesPayload{
		Integration: gi.Name, Login: link.Login, Repositories: res.Repositories, Truncated: res.Truncated,
	})
}

// writeGitHubError answers a failed GitHub-user call.
func (s *Server) writeGitHubError(w http.ResponseWriter, r *http.Request, gi ghlink.Integration, err error) {
	switch {
	case errors.Is(err, ghlink.ErrNotLinked):
		http.Error(w, "Your GitHub connection on "+gi.Host()+" has expired. Reconnect GitHub.", http.StatusForbidden)
	case errors.Is(err, ghuser.ErrUnavailable):
		http.Error(w, "GitHub linking is not configured.", http.StatusServiceUnavailable)
	default:
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "github user call failed",
			slog.String("integration", gi.Name), slog.Any("error", err))
		http.Error(w, "GitHub could not be reached through the integration-controller.", http.StatusBadGateway)
	}
}
