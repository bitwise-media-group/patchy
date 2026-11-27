// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghlink

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/bitwise-media-group/patchy/internal/ghuser"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
)

// Cookie names and bounds.
const (
	// cookiePrefix names one link cookie per Integration:
	// patchy-ghlink-<integration>. It is distinct from the flow's own
	// cookies, so no Integration name can collide with them.
	cookiePrefix = "patchy-ghlink-"
	// cookieState carries the CSRF half of the state double-submit for one
	// authorization round trip.
	cookieState = "patchy-github-state"
	// CookieError carries a human-readable link failure to the SPA. It is
	// not HttpOnly and carries no secret.
	CookieError = "patchy-github-error"
	// stateTTL bounds one authorization round trip.
	stateTTL = 10 * time.Minute
	// refreshSkew refreshes an access token this long before it expires.
	refreshSkew = time.Minute
	// refreshShare is how long a refresh outcome is shared with concurrent
	// callers presenting the same (now spent) refresh token.
	refreshShare = time.Minute
)

// ErrNotLinked reports that the user has no usable link for the
// Integration: none was made, it belongs to someone else, or GitHub rejected
// its tokens (the UI offers "Reconnect").
var ErrNotLinked = errors.New("github account not connected")

// Integration is a connectable github Integration: one with
// spec.github.userAuthorization and the UserAuthorizationReady condition.
type Integration struct {
	Name     string
	BaseURL  string
	ClientID string
}

// Host is where the Integration's repositories live.
func (i Integration) Host() string { return ghuser.Host(i.BaseURL) }

// Users is the slice of the integration-controller client the flow needs;
// ghuser.Client implements it.
type Users interface {
	Exchange(ctx context.Context, req ghuser.ExchangeRequest) (*ghuser.ExchangeResponse, error)
	Refresh(ctx context.Context, req ghuser.RefreshRequest) (*ghuser.Tokens, error)
}

// Config configures a Linker.
type Config struct {
	// Key seals the state and link cookies (auth.LinkKey).
	Key []byte
	// Secure sets the Secure cookie attribute.
	Secure bool
	// MaxAge caps a link cookie's lifetime: the session duration.
	MaxAge time.Duration
	// BaseURL is the status page's public origin, for the callback URL.
	BaseURL func(r *http.Request) string
	// Lookup resolves a connectable Integration by name.
	Lookup func(ctx context.Context, name string) (Integration, bool)
	// Identify resolves the signed-in user.
	Identify func(w http.ResponseWriter, r *http.Request) (*auth.Identity, error)
	// Users calls the integration-controller.
	Users Users
	Log   *slog.Logger
	// Now tells time; nil means time.Now.
	Now func() time.Time
}

// Linker runs the link flow and reads the link cookies.
type Linker struct {
	cfg Config

	mu        sync.Mutex
	refreshes map[[sha256.Size]byte]*refreshCall
}

// refreshCall is one refresh, shared by every caller presenting the same
// refresh token while it runs and for refreshShare after.
type refreshCall struct {
	done   chan struct{}
	tokens *ghuser.Tokens
	err    error
	at     time.Time
}

// New builds a Linker; nil when cfg has no key or no Users client (the
// feature is off).
func New(cfg Config) *Linker {
	if len(cfg.Key) == 0 || cfg.Users == nil {
		return nil
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Linker{cfg: cfg, refreshes: map[[sha256.Size]byte]*refreshCall{}}
}

// Link is the sealed payload of one link cookie.
type Link struct {
	Integration  string    `json:"integration"`
	Login        string    `json:"login"`
	UserID       int64     `json:"userID"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
	// LinkedTo is the patchy username the link was made for.
	LinkedTo string `json:"linkedTo"`
	// LinkedAt anchors the cookie lifetime: refreshing never extends a link
	// past LinkedAt + MaxAge.
	LinkedAt time.Time `json:"linkedAt"`
}

// state is the sealed OAuth state blob.
type state struct {
	Integration  string    `json:"integration"`
	Verifier     string    `json:"verifier"`
	CSRF         string    `json:"csrf"`
	LinkedTo     string    `json:"linkedTo"`
	OriginalPath string    `json:"originalPath"`
	IssuedAt     time.Time `json:"issuedAt"`
}

// CookieName is the link cookie of one Integration.
func CookieName(integration string) string { return cookiePrefix + integration }

// Register mounts the flow. Disconnect is POST-only so a cross-site GET
// cannot drop a link.
func (l *Linker) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /oauth2/github/{integration}/authorize", l.handleAuthorize)
	mux.HandleFunc("GET /oauth2/github/{integration}/callback", l.handleCallback)
	mux.HandleFunc("POST /oauth2/github/{integration}/disconnect", l.handleDisconnect)
}

func (l *Linker) callbackURL(r *http.Request, integration string) string {
	return strings.TrimSuffix(l.cfg.BaseURL(r), "/") + "/oauth2/github/" + integration + "/callback"
}

func (l *Linker) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("integration")
	id, err := l.cfg.Identify(w, r)
	if err != nil || id == nil {
		l.fail(w, r, "/", "Sign in before connecting GitHub.", err)
		return
	}
	integ, ok := l.cfg.Lookup(r.Context(), name)
	if !ok {
		l.fail(w, r, "/", fmt.Sprintf("Integration %q cannot connect GitHub accounts.", name), nil)
		return
	}
	csrf, err := auth.RandomToken()
	if err != nil {
		l.fail(w, r, "/", "Connecting GitHub could not start.", err)
		return
	}
	st := state{
		Integration:  integ.Name,
		Verifier:     oauth2.GenerateVerifier(),
		CSRF:         csrf,
		LinkedTo:     id.Username,
		OriginalPath: auth.SafePath(r.URL.Query().Get("originalPath")),
		IssuedAt:     l.cfg.Now(),
	}
	blob, err := auth.Seal(l.cfg.Key, st)
	if err != nil {
		l.fail(w, r, st.OriginalPath, "Connecting GitHub could not start.", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieState,
		Value:    csrf,
		Path:     "/oauth2/github/",
		MaxAge:   int(stateTTL.Seconds()),
		HttpOnly: true,
		Secure:   l.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	target := ghuser.AuthorizeURL(integ.BaseURL, integ.ClientID, l.callbackURL(r, integ.Name), blob,
		oauth2.S256ChallengeFromVerifier(st.Verifier))
	http.Redirect(w, r, target, http.StatusFound)
}

func (l *Linker) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := r.PathValue("integration")
	http.SetCookie(w, &http.Cookie{
		Name: cookieState, Value: "", Path: "/oauth2/github/", MaxAge: -1,
		HttpOnly: true, Secure: l.cfg.Secure, SameSite: http.SameSiteLaxMode,
	})
	var st state
	if err := auth.Unseal(l.cfg.Key, q.Get("state"), &st); err != nil {
		l.fail(w, r, "/", "The GitHub connection state was invalid; try again.", err)
		return
	}
	if e := q.Get("error"); e != "" {
		l.fail(w, r, st.OriginalPath, "GitHub declined the connection: "+
			strings.TrimSpace(e+" "+q.Get("error_description")), nil)
		return
	}
	if st.Integration != name {
		l.fail(w, r, st.OriginalPath, "The GitHub connection state was invalid; try again.",
			errors.New("state integration mismatch"))
		return
	}
	if l.cfg.Now().After(st.IssuedAt.Add(stateTTL)) {
		l.fail(w, r, st.OriginalPath, "Connecting GitHub took too long; try again.", nil)
		return
	}
	csrf, err := r.Cookie(cookieState)
	if err != nil || csrf.Value == "" || csrf.Value != st.CSRF {
		l.fail(w, r, st.OriginalPath, "The GitHub connection state did not match; try again.",
			errors.New("csrf double-submit failed"))
		return
	}
	id, err := l.cfg.Identify(w, r)
	if err != nil || id == nil || id.Username != st.LinkedTo {
		l.fail(w, r, st.OriginalPath, "You are signed in as someone else than when the connection started.", err)
		return
	}
	if _, ok := l.cfg.Lookup(r.Context(), name); !ok {
		l.fail(w, r, st.OriginalPath, fmt.Sprintf("Integration %q cannot connect GitHub accounts.", name), nil)
		return
	}
	res, err := l.cfg.Users.Exchange(r.Context(), ghuser.ExchangeRequest{
		Integration: name,
		Code:        q.Get("code"),
		Verifier:    st.Verifier,
		RedirectURI: l.callbackURL(r, name),
	})
	if err != nil {
		l.fail(w, r, st.OriginalPath, "GitHub did not accept the connection; try again.", err)
		return
	}
	link := Link{
		Integration:  name,
		Login:        res.Login,
		UserID:       res.UserID,
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		Expiry:       res.ExpiresAt,
		LinkedTo:     id.Username,
		LinkedAt:     l.cfg.Now(),
	}
	if err := l.write(w, &link); err != nil {
		l.fail(w, r, st.OriginalPath, "The GitHub connection could not be stored.", err)
		return
	}
	auth.ClearCookie(w, CookieError, l.cfg.Secure)
	l.cfg.Log.LogAttrs(r.Context(), slog.LevelInfo, "github account connected",
		slog.String("integration", name), slog.String("user", id.Username), slog.String("github.login", res.Login))
	redirectLocal(w, r, st.OriginalPath)
}

func (l *Linker) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("integration")
	auth.ClearCookie(w, CookieName(name), l.cfg.Secure)
	w.WriteHeader(http.StatusNoContent)
}

// fail records a link failure for the SPA and sends the browser back.
func (l *Linker) fail(w http.ResponseWriter, r *http.Request, back, msg string, err error) {
	l.cfg.Log.LogAttrs(r.Context(), slog.LevelWarn, "github connect failed",
		slog.String("integration", r.PathValue("integration")), slog.String("reason", msg), slog.Any("error", err))
	_ = auth.SetJSONCookie(w, CookieError, msg, time.Minute, l.cfg.Secure)
	redirectLocal(w, r, back)
}

// redirectLocal sends the browser back to p, a path on the status page.
// p comes from the sealed state, which authorize already cleaned with
// SafePath; it is checked again here so the redirect never rests on the
// seal alone.
func redirectLocal(w http.ResponseWriter, r *http.Request, p string) {
	if auth.IsLocalURL(p) {
		http.Redirect(w, r, p, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// write seals and stores a link cookie for the rest of its lifetime.
func (l *Linker) write(w http.ResponseWriter, link *Link) error {
	remaining := link.LinkedAt.Add(l.cfg.MaxAge).Sub(l.cfg.Now())
	if remaining <= 0 {
		auth.ClearCookie(w, CookieName(link.Integration), l.cfg.Secure)
		return ErrNotLinked
	}
	blob, err := auth.Seal(l.cfg.Key, link)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName(link.Integration),
		Value:    blob,
		Path:     "/",
		MaxAge:   int(remaining.Seconds()),
		HttpOnly: true,
		Secure:   l.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// Get reads the user's link for one Integration. A link made for another
// username, a tampered cookie, or one past its lifetime reads as absent.
func (l *Linker) Get(r *http.Request, id auth.Identity, integration string) (*Link, bool) {
	if l == nil {
		return nil, false
	}
	c, err := r.Cookie(CookieName(integration))
	if err != nil || c.Value == "" {
		return nil, false
	}
	var link Link
	if err := auth.Unseal(l.cfg.Key, c.Value, &link); err != nil {
		return nil, false
	}
	if link.LinkedTo != id.Username || link.Integration != integration ||
		l.cfg.Now().After(link.LinkedAt.Add(l.cfg.MaxAge)) {
		return nil, false
	}
	return &link, true
}

// Call runs fn with a live access token for the link, refreshing it first
// when it is about to expire, and once more if GitHub rejects it. A refresh
// GitHub rejects drops the link and returns ErrNotLinked. The link is
// updated in place, and its cookie rewritten, on every refresh.
func (l *Linker) Call(
	ctx context.Context, w http.ResponseWriter, link *Link, fn func(token string) error,
) error {
	if !link.Expiry.IsZero() && l.cfg.Now().Add(refreshSkew).After(link.Expiry) {
		if err := l.refresh(ctx, w, link); err != nil {
			return err
		}
	}
	err := fn(link.AccessToken)
	if !ghuser.IsCode(err, ghuser.CodeUserToken) {
		return err
	}
	if err := l.refresh(ctx, w, link); err != nil {
		return err
	}
	err = fn(link.AccessToken)
	if ghuser.IsCode(err, ghuser.CodeUserToken) {
		l.drop(w, link)
		return ErrNotLinked
	}
	return err
}

// refresh rotates the link's tokens, sharing one call among concurrent
// callers holding the same refresh token.
func (l *Linker) refresh(ctx context.Context, w http.ResponseWriter, link *Link) error {
	if link.RefreshToken == "" {
		l.drop(w, link)
		return ErrNotLinked
	}
	key := sha256.Sum256([]byte(link.Integration + "\x00" + link.RefreshToken))
	now := l.cfg.Now()
	l.mu.Lock()
	for k, c := range l.refreshes {
		select {
		case <-c.done:
			if now.Sub(c.at) > refreshShare {
				delete(l.refreshes, k)
			}
		default:
		}
	}
	call, shared := l.refreshes[key]
	if !shared {
		call = &refreshCall{done: make(chan struct{})}
		l.refreshes[key] = call
	}
	l.mu.Unlock()
	if !shared {
		call.tokens, call.err = l.cfg.Users.Refresh(context.WithoutCancel(ctx), ghuser.RefreshRequest{
			Integration: link.Integration, RefreshToken: link.RefreshToken,
		})
		call.at = l.cfg.Now()
		close(call.done)
	}
	select {
	case <-call.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if call.err != nil {
		if ghuser.IsCode(call.err, ghuser.CodeUserToken) || ghuser.IsCode(call.err, ghuser.CodeIntegration) {
			l.drop(w, link)
			return ErrNotLinked
		}
		return call.err
	}
	link.AccessToken, link.Expiry = call.tokens.AccessToken, call.tokens.ExpiresAt
	if call.tokens.RefreshToken != "" {
		link.RefreshToken = call.tokens.RefreshToken
	}
	return l.write(w, link)
}

// drop clears a link GitHub no longer honours.
func (l *Linker) drop(w http.ResponseWriter, link *Link) {
	auth.ClearCookie(w, CookieName(link.Integration), l.cfg.Secure)
	l.cfg.Log.LogAttrs(context.Background(), slog.LevelInfo, "github link dropped",
		slog.String("integration", link.Integration), slog.String("user", link.LinkedTo),
		slog.String("github.login", link.Login))
}
