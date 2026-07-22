// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Cookie names. The session and state cookies are HttpOnly and carry sealed
// data; the provider/error/logout cookies are SPA-visible and carry no
// secrets — they only tell the client what the sign-in surface looks like.
const (
	// cookieSession is the chunked session cookie; chunks after the first
	// are cookieSession-1, cookieSession-2, ….
	cookieSession = "patchy-auth"
	// cookieOAuthState carries the CSRF half of the state double-submit
	// during one authorization round trip.
	cookieOAuthState = "patchy-oauth2-state"
	// CookieProvider tells the SPA whether and how sign-in works:
	// {"provider","authenticated","autoLogin"}, base64url JSON.
	CookieProvider = "patchy-auth-provider"
	// CookieAuthError carries a human-readable sign-in failure to the SPA.
	CookieAuthError = "patchy-auth-error"
	// CookieLogout marks an explicit sign-out so autoLogin pauses for it.
	CookieLogout = "patchy-auth-logout"
)

// chunkSize keeps each cookie under the 4KB browser limit with headroom for
// the name and attributes; maxChunks bounds a session at ~35KB, enough for
// bloated ID tokens (large group lists).
const (
	chunkSize = 3500
	maxChunks = 10
)

// providerState is the SPA-visible sign-in surface descriptor.
type providerState struct {
	Provider      string `json:"provider"`
	Authenticated bool   `json:"authenticated"`
	AutoLogin     bool   `json:"autoLogin,omitempty"`
}

// chunkName returns the i-th chunk's cookie name.
func chunkName(i int) string {
	if i == 0 {
		return cookieSession
	}
	return cookieSession + "-" + strconv.Itoa(i)
}

// writeChunked splits value across the session cookie chunks. Leftover
// chunks from a previously longer session are cleared.
func writeChunked(w http.ResponseWriter, value string, maxAge time.Duration, secure bool) error {
	chunks := (len(value) + chunkSize - 1) / chunkSize
	if chunks > maxChunks {
		return fmt.Errorf("session cookie needs %d chunks, limit %d", chunks, maxChunks)
	}
	for i := range maxChunks {
		start := i * chunkSize
		if start >= len(value) {
			clearCookie(w, chunkName(i), secure)
			continue
		}
		end := min(start+chunkSize, len(value))
		http.SetCookie(w, &http.Cookie{
			Name:     chunkName(i),
			Value:    value[start:end],
			Path:     "/",
			MaxAge:   int(maxAge.Seconds()),
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
	return nil
}

// readChunked reassembles the session cookie value; "" means no session.
func readChunked(r *http.Request) string {
	var b strings.Builder
	for i := range maxChunks {
		c, err := r.Cookie(chunkName(i))
		if err != nil || c.Value == "" {
			break
		}
		b.WriteString(c.Value)
	}
	return b.String()
}

// clearChunked removes every session chunk.
func clearChunked(w http.ResponseWriter, secure bool) {
	for i := range maxChunks {
		clearCookie(w, chunkName(i), secure)
	}
}

// clearCookie expires one cookie. Deletion matches on name and path only, so
// the hardened attributes here never prevent the clear — they just keep the
// expiring replacement as locked down as what it removes.
func clearCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// setJSONCookie writes an SPA-visible base64url JSON cookie. Not HttpOnly by
// design — the SPA reads it; it must never carry a secret.
func setJSONCookie(w http.ResponseWriter, name string, v any, maxAge time.Duration, secure bool) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("cookie %s: %w", name, err)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// readJSONCookie decodes an SPA-visible cookie into v, reporting presence.
func readJSONCookie(r *http.Request, name string, v any) bool {
	c, err := r.Cookie(name)
	if err != nil || c.Value == "" {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}
