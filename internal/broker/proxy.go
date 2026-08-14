// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"
)

// transport is the shared upstream transport. ResponseHeaderTimeout bounds a
// hung upstream (headers, not the streamed body — SSE responses run for as
// long as the agent thinks); everything else keeps http.DefaultTransport's
// pooling behavior.
func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 5 * time.Minute
	return t
}

// route is one mounted upstream: its reverse proxy plus the config the
// handler needs around it.
type route struct {
	name     string
	upstream Upstream
	proxy    *httputil.ReverseProxy
}

// newRoute builds the reverse proxy for one upstream. The Rewrite points the
// request at the target (the mux already stripped the route prefix), drops
// the caller's broker token and any inbound authorization the upstream must
// not see, and the credential transport attaches the real credential last —
// so nothing caller-controlled can survive into the authenticated request.
func newRoute(name string, u Upstream, log *slog.Logger) *route {
	rt := &route{name: name, upstream: u}
	rt.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u.Target)
			pr.Out.Host = u.Target.Host
			stripCallerHeaders(pr.Out.Header)
		},
		Transport:     credentialTransport{next: newTransport(), cred: u.Credential},
		FlushInterval: -1, // stream: flush every write immediately
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.LogAttrs(r.Context(), slog.LevelError, "upstream error",
				slog.String("route", name), slog.Any("error", err))
			writeAPIError(w, http.StatusBadGateway, "api_error",
				fmt.Sprintf("egress broker: upstream %s unavailable", name))
		},
	}
	return rt
}

// stripCallerHeaders removes the broker token and every credential-shaped
// header the caller may have sent: the skip-auth CLI paths leave
// Authorization empty, but nothing stops a compromised pod from setting one,
// and a SigV4 signature must be computed over exactly the headers the broker
// controls.
func stripCallerHeaders(h http.Header) {
	h.Del(TokenHeader)
	h.Del("Authorization")
	h.Del("X-Api-Key")
	for name := range h {
		if strings.HasPrefix(strings.ToLower(name), "x-amz-") {
			h.Del(name)
		}
	}
}

// credentialTransport attaches the upstream credential immediately before the
// request leaves. Running here rather than in Rewrite lets a failed token
// fetch surface through the proxy's ErrorHandler.
type credentialTransport struct {
	next http.RoundTripper
	cred CredentialFunc
}

func (t credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.cred != nil {
		if err := t.cred(req.Context(), req); err != nil {
			return nil, fmt.Errorf("attach credential: %w", err)
		}
	}
	return t.next.RoundTrip(req)
}

// bufferBody reads the whole request body into memory (bounded) so a signing
// credential can hash the payload. It reports whether the body fit.
func bufferBody(r *http.Request, limit int64) (bool, error) {
	if r.Body == nil {
		return true, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return true, err
	}
	if int64(len(body)) > limit {
		return false, nil
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return true, nil
}

// writeAPIError emits an Anthropic-style JSON error envelope, which is what
// every client on these routes knows how to surface.
func writeAPIError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": kind, "message": message},
	})
}
