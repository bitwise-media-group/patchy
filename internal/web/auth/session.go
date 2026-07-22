// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package auth

import (
	"net/http"
	"time"
)

// session is the sealed payload of the chunked HttpOnly session cookie.
// There is no server-side session store: the cookie is the session, and the
// seal key (derived from the client secret) is what invalidates it wholesale.
type session struct {
	// IDToken is the OIDC ID token, re-verified on every request.
	IDToken string `json:"idToken"`
	// RefreshToken renews an expired ID token mid-session.
	RefreshToken string `json:"refreshToken,omitempty"`
	// Start anchors the absolute session lifetime: refreshing tokens never
	// extends a session past Start + sessionDuration.
	Start time.Time `json:"start"`
}

// writeSession seals and writes the session cookie. The cookie's remaining
// lifetime always counts from the session start, so a token refresh does not
// quietly extend the session.
func (a *oidcAuthenticator) writeSession(w http.ResponseWriter, s session) error {
	blob, err := seal(a.key, s)
	if err != nil {
		return err
	}
	remaining := time.Until(s.Start.Add(a.cfg.SessionDuration.Duration))
	if remaining <= 0 {
		clearChunked(w, a.secure())
		return nil
	}
	return writeChunked(w, blob, remaining, a.secure())
}

// readSession reads and unseals the session cookie; ok is false when there
// is no (valid) session.
func (a *oidcAuthenticator) readSession(r *http.Request) (session, bool) {
	blob := readChunked(r)
	if blob == "" {
		return session{}, false
	}
	var s session
	if err := unseal(a.key, blob, &s); err != nil {
		return session{}, false
	}
	if a.now().After(s.Start.Add(a.cfg.SessionDuration.Duration)) {
		return session{}, false
	}
	return s, true
}
