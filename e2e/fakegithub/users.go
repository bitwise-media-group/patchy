// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package fakegithub

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The GitHub App surface: the App's own JWT-authenticated endpoints, its
// installation tokens and repository inventory, and the user-to-server
// OAuth flow (authorize, the token endpoint with PKCE and single-use
// refresh-token rotation) with the calls a user token makes. Only one App
// is modelled, installed once on one account.

// App is the fake's one GitHub App.
type App struct {
	// ID is the App ID the credential Secret's appID carries; JWTs must be
	// issued by it.
	ID int64
	// ClientID and ClientSecret are the App's OAuth client credentials.
	ClientID     string
	ClientSecret string
	// PublicKey verifies the App's JWTs; nil skips the signature check.
	PublicKey *rsa.PublicKey
	// Account is the organization the App is installed on.
	Account string
	// InstallationID is that installation's ID (default 1).
	InstallationID int64
	// CallbackURLs are the App's registered callback URLs. Like GitHub,
	// authorize redirects only to one of them, so a redirect_uri patchy
	// gets wrong fails here as it would there. Matching is exact (GitHub
	// also admits a subdirectory of one). AddCallbackURL adds one later,
	// for a server whose address is known only once it is listening.
	CallbackURLs []string
}

// Repository roles a user may hold, highest first.
const (
	RoleAdmin    = "admin"
	RoleMaintain = "maintain"
	RoleWrite    = "write"
	RoleTriage   = "triage"
	RoleRead     = "read"
)

// userCodeTTL bounds an authorization code, as GitHub's ten minutes do.
const userCodeTTL = 10 * time.Minute

// userTokenPrefix marks a user access token, as GitHub's ghu_ does.
const userTokenPrefix = "ghu_"

// userTokenTTL is the access token lifetime the token endpoint reports.
const userTokenTTL = 8 * time.Hour

type fakeUser struct {
	login string
	id    int64
	roles map[string]string // "owner/name" → role
}

type authCode struct {
	login, redirectURI, challenge string
	expires                       time.Time
}

// userState is the App and user record.
type userState struct {
	app         *App
	users       map[string]*fakeUser
	nextUserID  int64
	codes       map[string]authCode
	access      map[string]string // access token → login
	refresh     map[string]string // refresh token → login
	instTokens  map[string]int64  // installation token → installation ID
	browserUser string
	refreshes   int
	seq         int
}

func newUserState() userState {
	return userState{
		users:      map[string]*fakeUser{},
		nextUserID: 5000,
		codes:      map[string]authCode{},
		access:     map[string]string{},
		refresh:    map[string]string{},
		instTokens: map[string]int64{},
	}
}

// RegisterApp installs the fake's GitHub App. Without one, the App and
// OAuth endpoints answer 404.
func (s *Server) RegisterApp(app App) {
	if app.InstallationID == 0 {
		app.InstallationID = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.user.app = &app
}

// AddCallbackURL registers one more callback URL on the App.
func (s *Server) AddCallbackURL(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.user.app != nil {
		s.user.app.CallbackURLs = append(s.user.app.CallbackURLs, u)
	}
}

// callbackURL returns the registered callback URL that redirectURI names,
// or "" when it names none.
func (a *App) callbackURL(redirectURI string) string {
	for _, u := range a.CallbackURLs {
		if u == redirectURI {
			return u
		}
	}
	return ""
}

// userLocked returns (creating) the user record for login. Callers hold
// s.mu.
func (s *Server) userLocked(login string) *fakeUser {
	u, ok := s.user.users[login]
	if !ok {
		s.user.nextUserID++
		u = &fakeUser{login: login, id: s.user.nextUserID, roles: map[string]string{}}
		s.user.users[login] = u
	}
	return u
}

// SetUserRole gives login a role on repo ("owner/name"): admin, maintain,
// write, triage or read. An empty role removes the user's access. The
// repository joins the App installation's inventory.
func (s *Server) SetUserRole(login, repo, role string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.userLocked(login)
	if role == "" {
		delete(u.roles, repo)
		return
	}
	u.roles[repo] = role
}

// UserID returns login's numeric GitHub ID (creating the user).
func (s *Server) UserID(login string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userLocked(login).id
}

// SetBrowserUser names the GitHub user signed in to the fake's web UI: the
// one the authorize endpoint approves for when the request names no login.
func (s *Server) SetBrowserUser(login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.user.browserUser = login
}

// ExpireUserTokens revokes every user access token (refresh tokens stay
// valid), so the next user call answers 401 and the caller must refresh.
func (s *Server) ExpireUserTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.user.access)
}

// TokenRefreshes counts the refresh-token grants the token endpoint has
// honoured.
func (s *Server) TokenRefreshes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.user.refreshes
}

func (s *Server) userRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app", s.getApp)
	mux.HandleFunc("GET /app/installations", s.listAppInstallations)
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", s.createInstallationToken)
	mux.HandleFunc("GET /repos/{owner}/{repo}/installation", s.getRepoInstallation)
	mux.HandleFunc("GET /installation/repositories", s.listInstallationRepos)
	mux.HandleFunc("GET /user", s.getUser)
	mux.HandleFunc("GET /user/installations", s.listUserInstallations)
	mux.HandleFunc("GET /user/installations/{id}/repositories", s.listUserInstallationRepos)
}

// webRoutes registers the OAuth endpoints, which GitHub serves on the web
// root rather than under the API.
func (s *Server) webRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login/oauth/authorize", s.authorize)
	mux.HandleFunc("POST /login/oauth/access_token", s.accessToken)
}

// credential is the request's bearer or "token" credential.
func credential(r *http.Request) string {
	h := r.Header.Get("Authorization")
	for _, p := range []string{"Bearer ", "bearer ", "token ", "Token "} {
		if v, ok := strings.CutPrefix(h, p); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func writeStatus(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

// appAuth checks the request carries a JWT issued by the registered App,
// answering the failure itself; app is nil when it has.
func (s *Server) appAuth(w http.ResponseWriter, r *http.Request) *App {
	s.mu.Lock()
	app := s.user.app
	s.mu.Unlock()
	if app == nil {
		writeStatus(w, http.StatusNotFound, "fakegithub: no GitHub App registered")
		return nil
	}
	if err := verifyAppJWT(credential(r), app, time.Now()); err != nil {
		writeStatus(w, http.StatusUnauthorized, "A JSON web token could not be decoded: "+err.Error())
		return nil
	}
	return app
}

// verifyAppJWT checks an RS256 App JWT: shape, signature (when the App has
// a public key), issuer and expiry.
func verifyAppJWT(tok string, app *App, now time.Time) error {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return fmt.Errorf("not a JWT")
	}
	if app.PublicKey != nil {
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			return fmt.Errorf("signature encoding: %w", err)
		}
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(app.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
			return fmt.Errorf("signature: %w", err)
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("claims encoding: %w", err)
	}
	var claims struct {
		Iss json.RawMessage `json:"iss"`
		Exp int64           `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return fmt.Errorf("claims: %w", err)
	}
	if strings.Trim(string(claims.Iss), `"`) != strconv.FormatInt(app.ID, 10) {
		return fmt.Errorf("issuer %s is not app %d", claims.Iss, app.ID)
	}
	if claims.Exp != 0 && now.Unix() > claims.Exp {
		return fmt.Errorf("expired")
	}
	return nil
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request) {
	app := s.appAuth(w, r)
	if app == nil {
		return
	}
	writeJSON(w, map[string]any{"id": app.ID, "slug": "patchy-e2e", "name": "patchy e2e", "client_id": app.ClientID})
}

func installationBody(app *App) map[string]any {
	return map[string]any{
		"id":          app.InstallationID,
		"app_id":      app.ID,
		"target_type": "Organization",
		"account":     map[string]any{"login": app.Account, "type": "Organization"},
		"permissions": map[string]string{
			"contents": "write", "issues": "write", "security_events": "write", "secret_scanning_alerts": "write",
		},
	}
}

func (s *Server) listAppInstallations(w http.ResponseWriter, r *http.Request) {
	app := s.appAuth(w, r)
	if app == nil {
		return
	}
	writeJSON(w, []map[string]any{installationBody(app)})
}

func (s *Server) createInstallationToken(w http.ResponseWriter, r *http.Request) {
	app := s.appAuth(w, r)
	if app == nil {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if id != app.InstallationID {
		writeStatus(w, http.StatusNotFound, "Not Found")
		return
	}
	s.mu.Lock()
	s.user.seq++
	tok := fmt.Sprintf("ghs_e2e_%d_%d", id, s.user.seq)
	s.user.instTokens[tok] = id
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      tok,
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
}

func (s *Server) getRepoInstallation(w http.ResponseWriter, r *http.Request) {
	app := s.appAuth(w, r)
	if app == nil {
		return
	}
	if !strings.EqualFold(r.PathValue("owner"), app.Account) {
		writeStatus(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, installationBody(app))
}

// inventoryLocked is the installation's repositories: every repository the
// fake knows of (seeded alerts, user roles) on the App's account, sorted.
// Callers hold s.mu.
func (s *Server) inventoryLocked() []string {
	if s.user.app == nil {
		return nil
	}
	seen := map[string]bool{}
	add := func(owner, repo string) {
		if strings.EqualFold(owner, s.user.app.Account) {
			seen[owner+"/"+repo] = true
		}
	}
	for _, a := range s.alerts {
		add(a.owner, a.repo)
	}
	for _, a := range s.secrets.alerts {
		add(a.owner, a.repo)
	}
	for _, u := range s.user.users {
		for full := range u.roles {
			owner, repo, _ := strings.Cut(full, "/")
			add(owner, repo)
		}
	}
	out := make([]string, 0, len(seen))
	for full := range seen {
		out = append(out, full)
	}
	slices.Sort(out)
	return out
}

func repoBody(full string) map[string]any {
	owner, name, _ := strings.Cut(full, "/")
	return map[string]any{
		"name":           name,
		"full_name":      full,
		"owner":          map[string]any{"login": owner},
		"default_branch": "main",
		"private":        true,
	}
}

func (s *Server) listInstallationRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	_, ok := s.user.instTokens[credential(r)]
	inv := s.inventoryLocked()
	s.mu.Unlock()
	if !ok {
		writeStatus(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	repos := make([]map[string]any, 0, len(inv))
	for _, full := range inv {
		repos = append(repos, repoBody(full))
	}
	writeJSON(w, map[string]any{"total_count": len(repos), "repositories": repos})
}

// tokenUser resolves the request's user access token, or nil.
func (s *Server) tokenUser(r *http.Request) *fakeUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	login, ok := s.user.access[credential(r)]
	if !ok {
		return nil
	}
	return s.userLocked(login)
}

// userAuth requires a user access token, answering 401 itself.
func (s *Server) userAuth(w http.ResponseWriter, r *http.Request) *fakeUser {
	u := s.tokenUser(r)
	if u == nil {
		writeStatus(w, http.StatusUnauthorized, "Bad credentials")
	}
	return u
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	u := s.userAuth(w, r)
	if u == nil {
		return
	}
	writeJSON(w, map[string]any{"login": u.login, "id": u.id, "type": "User"})
}

func (s *Server) listUserInstallations(w http.ResponseWriter, r *http.Request) {
	u := s.userAuth(w, r)
	if u == nil {
		return
	}
	s.mu.Lock()
	app := s.user.app
	s.mu.Unlock()
	installs := []map[string]any{}
	if app != nil {
		installs = append(installs, installationBody(app))
	}
	writeJSON(w, map[string]any{"total_count": len(installs), "installations": installs})
}

// permissions is GitHub's per-user permission flags for a role.
func permissions(role string) map[string]bool {
	rank := map[string]int{RoleRead: 1, RoleTriage: 2, RoleWrite: 3, RoleMaintain: 4, RoleAdmin: 5}[role]
	return map[string]bool{
		"pull":     rank >= 1,
		"triage":   rank >= 2,
		"push":     rank >= 3,
		"maintain": rank >= 4,
		"admin":    rank >= 5,
	}
}

func (s *Server) listUserInstallationRepos(w http.ResponseWriter, r *http.Request) {
	u := s.userAuth(w, r)
	if u == nil {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	app := s.user.app
	inv := s.inventoryLocked()
	roles := make(map[string]string, len(u.roles))
	for k, v := range u.roles {
		roles[k] = v
	}
	s.mu.Unlock()
	if app == nil || id != app.InstallationID {
		writeStatus(w, http.StatusNotFound, "Not Found")
		return
	}
	// A user token sees the installation's repositories the user can
	// access, with the user's own permissions on each.
	repos := []map[string]any{}
	for _, full := range inv {
		role, ok := roles[full]
		if !ok {
			continue
		}
		body := repoBody(full)
		body["permissions"] = permissions(role)
		repos = append(repos, body)
	}
	writeJSON(w, map[string]any{"total_count": len(repos), "repositories": repos})
}

// getRepo serves GET /repos/{owner}/{repo}. With a user token it carries
// the user's permissions, and a repository the user cannot see is a 404,
// as on GitHub; any other credential gets the plain repository.
func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	full := r.PathValue("owner") + "/" + r.PathValue("repo")
	u := s.tokenUser(r)
	if u == nil {
		// A spent or revoked user token is refused, never mistaken for a
		// credential without a user.
		if strings.HasPrefix(credential(r), userTokenPrefix) {
			writeStatus(w, http.StatusUnauthorized, "Bad credentials")
			return
		}
		writeJSON(w, map[string]any{"default_branch": "main"})
		return
	}
	s.mu.Lock()
	role, ok := u.roles[full]
	s.mu.Unlock()
	if !ok {
		writeStatus(w, http.StatusNotFound, "Not Found")
		return
	}
	body := repoBody(full)
	body["permissions"] = permissions(role)
	writeJSON(w, body)
}

// authorize stands in for GitHub's consent page: it approves at once for
// the signed-in browser user (SetBrowserUser, or the login query
// parameter) and redirects back with a code bound to the PKCE challenge
// and redirect URI.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	app := s.user.app
	if app == nil || q.Get("client_id") != app.ClientID {
		http.Error(w, "fakegithub: unknown client_id", http.StatusNotFound)
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "fakegithub: patchy must send an S256 PKCE challenge", http.StatusBadRequest)
		return
	}
	// The redirect goes to the registered URL, never to the request's own
	// value: an authorize endpoint that redirects anywhere it is told is an
	// open redirect.
	registered := app.callbackURL(q.Get("redirect_uri"))
	if registered == "" {
		http.Error(w, "fakegithub: redirect_uri is not one of the App's callback URLs", http.StatusBadRequest)
		return
	}
	redirect, err := url.Parse(registered)
	if err != nil {
		http.Error(w, "fakegithub: bad callback URL: "+err.Error(), http.StatusInternalServerError)
		return
	}
	login := q.Get("login")
	if login == "" {
		login = s.user.browserUser
	}
	if login == "" {
		http.Error(w, "fakegithub: no signed-in GitHub user (SetBrowserUser or ?login=)", http.StatusUnauthorized)
		return
	}
	s.userLocked(login)
	s.user.seq++
	code := fmt.Sprintf("code_e2e_%d", s.user.seq)
	s.user.codes[code] = authCode{
		login: login, redirectURI: redirect.String(), challenge: q.Get("code_challenge"),
		expires: time.Now().Add(userCodeTTL),
	}
	back := redirect.Query()
	back.Set("code", code)
	back.Set("state", q.Get("state"))
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// oauthError answers the token endpoint's error shape.
func oauthError(w http.ResponseWriter, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

// accessToken is POST /login/oauth/access_token: the authorization-code
// grant (single-use code, redirect URI and PKCE verifier checked) and the
// refresh-token grant (each refresh token honoured once, rotated with the
// access token).
func (s *Server) accessToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request", err.Error())
		return
	}
	f := r.PostForm
	s.mu.Lock()
	defer s.mu.Unlock()
	app := s.user.app
	if app == nil || f.Get("client_id") != app.ClientID || f.Get("client_secret") != app.ClientSecret {
		oauthError(w, "incorrect_client_credentials", "The client_id and/or client_secret passed are incorrect.")
		return
	}
	var login string
	switch f.Get("grant_type") {
	case "", "authorization_code":
		c, ok := s.user.codes[f.Get("code")]
		delete(s.user.codes, f.Get("code"))
		switch {
		case !ok || time.Now().After(c.expires):
			oauthError(w, "bad_verification_code", "The code passed is incorrect or expired.")
			return
		case f.Get("redirect_uri") != c.redirectURI:
			oauthError(w, "redirect_uri_mismatch", "The redirect_uri does not match the authorize request.")
			return
		case pkceChallenge(f.Get("code_verifier")) != c.challenge:
			oauthError(w, "bad_verification_code", "The code_verifier does not match the code_challenge.")
			return
		}
		login = c.login
	case "refresh_token":
		l, ok := s.user.refresh[f.Get("refresh_token")]
		if !ok {
			oauthError(w, "bad_refresh_token", "The refresh token passed is incorrect or expired.")
			return
		}
		// Rotation: the refresh token is spent, and the access tokens it
		// stood behind go with it.
		delete(s.user.refresh, f.Get("refresh_token"))
		for tok, owner := range s.user.access {
			if owner == l {
				delete(s.user.access, tok)
			}
		}
		s.user.refreshes++
		login = l
	default:
		oauthError(w, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
		return
	}
	s.user.seq++
	access := fmt.Sprintf("%se2e_%d", userTokenPrefix, s.user.seq)
	refresh := fmt.Sprintf("ghr_e2e_%d", s.user.seq)
	s.user.access[access] = login
	s.user.refresh[refresh] = login
	writeJSON(w, map[string]any{
		"access_token":             access,
		"token_type":               "bearer",
		"scope":                    "",
		"expires_in":               int(userTokenTTL.Seconds()),
		"refresh_token":            refresh,
		"refresh_token_expires_in": int((180 * 24 * time.Hour).Seconds()),
	})
}

// pkceChallenge is the S256 challenge of a verifier.
func pkceChallenge(verifier string) string {
	if verifier == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
