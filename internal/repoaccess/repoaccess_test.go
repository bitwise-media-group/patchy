// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package repoaccess

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidRepository(t *testing.T) {
	cases := map[string]bool{
		"acme/shop":                        true,
		"acme-co/shop.web_v2":              true,
		"a/b":                              true,
		"acme/":                            false,
		"/shop":                            false,
		"acme":                             false,
		"acme/shop/extra":                  false,
		"acme /shop":                       false,
		"acme/..":                          false,
		"-acme/shop":                       false,
		"acme/" + strings.Repeat("x", 200): false,
	}
	for in, want := range cases {
		if got := ValidRepository(in); got != want {
			t.Errorf("ValidRepository(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestClientCheck(t *testing.T) {
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != Path {
			http.NotFound(w, r)
			return
		}
		tokens = append(tokens, r.Header.Get(TokenHeader))
		var req Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := Response{}
		for _, repo := range req.Repositories {
			out.Results = append(out.Results, Result{Repo: repo, Integrations: []IntegrationAccess{{
				Name: "gh", Alerts: Alerts{CodeScanning: &Check{Status: StatusOK}},
			}}})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "token")
	write := func(tok string) {
		if err := os.WriteFile(tokenFile, []byte(tok+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := &Client{URL: srv.URL + "/", TokenFile: tokenFile}

	write("first")
	res, err := c.Check(t.Context(), []string{"acme/shop"})
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := res["acme/shop"].Backfillable(); !ok || name != "gh" {
		t.Fatalf("result = %+v", res)
	}
	// The kubelet rotates the projected token: every call reads it again.
	write("second")
	if _, err := c.Check(t.Context(), []string{"acme/shop"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(tokens, ",") != "first,second" {
		t.Fatalf("tokens sent = %v, want first,second", tokens)
	}

	if _, err := c.Check(t.Context(), []string{"acme/"}); err == nil {
		t.Error("prefix entry accepted")
	}
	if _, err := (&Client{}).Check(t.Context(), []string{"acme/shop"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unconfigured client err = %v, want ErrUnavailable", err)
	}
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "token rejected", http.StatusUnauthorized)
	}))
	defer denied.Close()
	if _, err := (&Client{URL: denied.URL, TokenFile: tokenFile}).Check(t.Context(), []string{"a/b"}); err == nil ||
		!strings.Contains(err.Error(), "401") {
		t.Errorf("denied err = %v, want HTTP 401", err)
	}
}
