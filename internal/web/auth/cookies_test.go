// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// carry moves the recorder's cookies onto a fresh request, like a browser.
func carry(t *testing.T, rec *httptest.ResponseRecorder) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge >= 0 && c.Value != "" {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	return req
}

func TestChunkedCookiesRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{"small", 100},
		{"exactly one chunk", chunkSize},
		{"two chunks", chunkSize + 1},
		{"many chunks", chunkSize*4 + 17},
		{"maximum", chunkSize * maxChunks},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := strings.Repeat("a", tc.size-1) + "z"
			rec := httptest.NewRecorder()
			if err := writeChunked(rec, value, time.Hour, true); err != nil {
				t.Fatalf("writeChunked: %v", err)
			}
			for _, c := range rec.Result().Cookies() {
				if strings.HasPrefix(c.Name, cookieSession) && c.Value != "" {
					if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
						t.Errorf("chunk %s attributes: httpOnly=%v secure=%v sameSite=%v",
							c.Name, c.HttpOnly, c.Secure, c.SameSite)
					}
				}
			}
			if got := readChunked(carry(t, rec)); got != value {
				t.Errorf("round trip lost data: got %d bytes, want %d", len(got), len(value))
			}
		})
	}
}

func TestChunkedCookiesTooLarge(t *testing.T) {
	oversized := strings.Repeat("a", chunkSize*maxChunks+1)
	if err := writeChunked(httptest.NewRecorder(), oversized, time.Hour, true); err == nil {
		t.Error("oversized session accepted")
	}
}

func TestWriteChunkedClearsLeftovers(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := writeChunked(rec, strings.Repeat("a", chunkSize*3), time.Hour, true); err != nil {
		t.Fatalf("writeChunked: %v", err)
	}
	// Shrink to one chunk; the request still carries the old chunks.
	req := carry(t, rec)
	rec2 := httptest.NewRecorder()
	if err := writeChunked(rec2, "short", time.Hour, true); err != nil {
		t.Fatalf("writeChunked: %v", err)
	}
	// Apply rec2's cookies over req's like a jar: replacements replace,
	// cleared chunks disappear.
	jar := map[string]string{}
	for _, c := range req.Cookies() {
		jar[c.Name] = c.Value
	}
	for _, c := range rec2.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(jar, c.Name)
			continue
		}
		if c.Value != "" {
			jar[c.Name] = c.Value
		}
	}
	merged := httptest.NewRequest(http.MethodGet, "/", nil)
	for name, value := range jar {
		merged.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	if got := readChunked(merged); got != "short" {
		t.Errorf("after shrink read %q, want %q", got, "short")
	}
}

func TestClearCookieAttributes(t *testing.T) {
	rec := httptest.NewRecorder()
	clearCookie(rec, cookieSession, true)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("cookie not cleared: maxAge=%d value=%q", c.MaxAge, c.Value)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("clear attributes: httpOnly=%v secure=%v sameSite=%v", c.HttpOnly, c.Secure, c.SameSite)
	}
}

func TestJSONCookieRoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	want := providerState{Provider: "oidc", Authenticated: true, AutoLogin: true}
	if err := setJSONCookie(rec, CookieProvider, want, 0, true); err != nil {
		t.Fatalf("setJSONCookie: %v", err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name != CookieProvider {
			continue
		}
		if c.HttpOnly {
			t.Error("provider cookie must be SPA-readable (not HttpOnly)")
		}
		if !c.Secure {
			t.Error("provider cookie must carry the Secure attribute")
		}
	}
	var got providerState
	if !readJSONCookie(carry(t, rec), CookieProvider, &got) || got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
