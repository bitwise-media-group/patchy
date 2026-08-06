// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package generic

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// recordedRequest is one call the fake external process saw.
type recordedRequest struct {
	signature string
	body      []byte
}

// fakeProcess runs an httptest server answering with status and body,
// recording every request.
func fakeProcess(t *testing.T, status int, body string) (*httptest.Server, *[]recordedRequest) {
	t.Helper()
	var got []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		got = append(got, recordedRequest{signature: r.Header.Get(pkggeneric.SignatureHeader), body: payload})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func verifySignature(t *testing.T, secret []byte, r recordedRequest) {
	t.Helper()
	mac := hmac.New(sha256.New, secret)
	mac.Write(r.body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if r.signature != want {
		t.Errorf("signature = %q, want %q over the exact body", r.signature, want)
	}
}

func TestNewClientValidates(t *testing.T) {
	if _, err := NewClient(ClientOptions{Secret: []byte("s")}); err == nil {
		t.Error("NewClient(no url) = nil error, want one")
	}
	if _, err := NewClient(ClientOptions{URL: "https://x"}); err == nil {
		t.Error("NewClient(no secret) = nil error, want one")
	}
}

func TestClientEnhance(t *testing.T) {
	secret := []byte("shared")
	srv, got := fakeProcess(t, http.StatusOK,
		`{"owners":["alice"],"commentMarkdown":"owned by payments","attributes":{"tier":"1"}}`)
	c, err := NewClient(ClientOptions{URL: srv.URL, Secret: secret})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	req := pkggeneric.EnhanceRequest{
		Version:     pkggeneric.Version,
		Integration: "cmdb",
		Issue:       pkggeneric.Issue{Title: "Reflected XSS", Repo: &pkggeneric.Repo{Owner: "acme", Name: "orders"}},
	}
	resp, err := c.Enhance(context.Background(), req)
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	if resp == nil || len(resp.Owners) != 1 || resp.Owners[0] != "alice" || resp.Attributes["tier"] != "1" {
		t.Errorf("Enhance() = %+v, want the endpoint's enrichment", resp)
	}
	if len(*got) != 1 {
		t.Fatalf("endpoint saw %d requests, want 1", len(*got))
	}
	verifySignature(t, secret, (*got)[0])
	var sent pkggeneric.EnhanceRequest
	if err := json.Unmarshal((*got)[0].body, &sent); err != nil {
		t.Fatalf("decode sent request: %v", err)
	}
	if sent.Version != "v1" || sent.Integration != "cmdb" || sent.Issue.Title != "Reflected XSS" {
		t.Errorf("sent request = %+v, want the issue view under version v1", sent)
	}
}

func TestClientEnhanceNothingToContribute(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"204", http.StatusNoContent, ""},
		{"200 empty body", http.StatusOK, ""},
		{"200 whitespace", http.StatusOK, "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeProcess(t, tt.status, tt.body)
			c, _ := NewClient(ClientOptions{URL: srv.URL, Secret: []byte("s")})
			resp, err := c.Enhance(context.Background(), pkggeneric.EnhanceRequest{})
			if err != nil || resp != nil {
				t.Errorf("Enhance() = (%+v, %v), want (nil, nil)", resp, err)
			}
		})
	}
}

func TestClientEnhanceErrors(t *testing.T) {
	srv, _ := fakeProcess(t, http.StatusInternalServerError, strings.Repeat("boom ", 200))
	c, _ := NewClient(ClientOptions{URL: srv.URL, Secret: []byte("s")})
	_, err := c.Enhance(context.Background(), pkggeneric.EnhanceRequest{})
	if err == nil {
		t.Fatal("Enhance(500) = nil error, want one")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want the status in it", err)
	}
	if len(err.Error()) > 400 {
		t.Errorf("error is %d chars; the response body must be summarized", len(err.Error()))
	}

	undecodable, _ := fakeProcess(t, http.StatusOK, "not json")
	c, _ = NewClient(ClientOptions{URL: undecodable.URL, Secret: []byte("s")})
	if _, err := c.Enhance(context.Background(), pkggeneric.EnhanceRequest{}); err == nil {
		t.Error("Enhance(undecodable body) = nil error, want one")
	}
}

func TestClientTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(slow.Close)
	c, _ := NewClient(ClientOptions{URL: slow.URL, Secret: []byte("s"), Timeout: 50 * time.Millisecond})
	start := time.Now()
	if _, err := c.Enhance(context.Background(), pkggeneric.EnhanceRequest{}); err == nil {
		t.Error("Enhance(hung endpoint) = nil error, want a deadline error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("call took %v; the configured timeout did not bind", elapsed)
	}
}

func TestResolverResolve(t *testing.T) {
	secret := []byte("shared")
	srv, got := fakeProcess(t, http.StatusOK, "")
	c, _ := NewClient(ClientOptions{URL: srv.URL, Secret: secret})
	r := NewResolver(c, "warehouse")

	alerts := []source.AlertRef{
		{ID: "wh-1001", Source: "warehouse", URL: "https://warehouse.internal/findings/1001"},
		{ID: "wh-1002", Source: "warehouse"},
	}
	v := source.Verdict{Kind: source.VerdictIgnore, Reason: "false positive", Comment: "not exploitable"}
	if err := r.Resolve(context.Background(), alerts, v); err != nil {
		t.Fatalf("Resolve() = %v, want nil", err)
	}
	if len(*got) != 1 {
		t.Fatalf("endpoint saw %d requests, want 1", len(*got))
	}
	verifySignature(t, secret, (*got)[0])
	var sent pkggeneric.ResolveRequest
	if err := json.Unmarshal((*got)[0].body, &sent); err != nil {
		t.Fatalf("decode sent request: %v", err)
	}
	if sent.Version != "v1" || sent.Integration != "warehouse" {
		t.Errorf("sent = %+v, want version and integration stamped", sent)
	}
	if len(sent.Alerts) != 2 || sent.Alerts[0].ID != "wh-1001" || sent.Alerts[0].URL == "" {
		t.Errorf("alerts = %+v, want both with ids and urls", sent.Alerts)
	}
	if sent.Verdict.Kind != "ignore" || sent.Verdict.Reason != "false positive" {
		t.Errorf("verdict = %+v, want the ignore verdict", sent.Verdict)
	}

	failing, _ := fakeProcess(t, http.StatusBadGateway, "upstream down")
	c, _ = NewClient(ClientOptions{URL: failing.URL, Secret: secret})
	if err := NewResolver(c, "warehouse").Resolve(context.Background(), alerts, v); err == nil {
		t.Error("Resolve(502) = nil error, want one so the reconciler retries")
	}
}
