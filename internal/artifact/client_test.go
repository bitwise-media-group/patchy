// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package artifact

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// blobServer records the last request and answers with a canned status.
func blobServer(t *testing.T, status int, gotAuth *string, gotBody *string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/blobs/"+testBlobDigest {
			t.Errorf("path = %q, want /internal/blobs/%s", r.URL.Path, testBlobDigest)
		}
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		if gotBody != nil {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
			}
			*gotBody = string(raw)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(ts.Close)
	return ts
}

const testBlobDigest = "cc11bb22cc33dd44ee55ff6600112233445566778899aabbccddeeff00112233"

func TestClientStat(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		want    bool
		wantErr bool
	}{
		{"cached 204", http.StatusNoContent, true, false},
		{"cached 200", http.StatusOK, true, false},
		{"missing 404", http.StatusNotFound, false, false},
		{"server error", http.StatusInternalServerError, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := blobServer(t, c.status, nil, nil)
			// Trailing slash on BaseURL must not double the separator.
			cl := &Client{BaseURL: ts.URL + "/"}
			got, err := cl.Stat(t.Context(), testBlobDigest)
			if (err != nil) != c.wantErr {
				t.Fatalf("Stat error = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("Stat = %t, want %t", got, c.want)
			}
		})
	}
}

func TestClientPut(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr error
		anyErr  bool
	}{
		{"created", http.StatusCreated, nil, false},
		{"already cached", http.StatusOK, nil, false},
		{"digest mismatch", http.StatusUnprocessableEntity, ErrDigestMismatch, false},
		{"too large", http.StatusRequestEntityTooLarge, ErrBlobTooLarge, false},
		{"server error", http.StatusInternalServerError, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var body string
			ts := blobServer(t, c.status, nil, &body)
			cl := &Client{BaseURL: ts.URL}
			err := cl.Put(t.Context(), testBlobDigest, strings.NewReader("tarball"), 7)
			switch {
			case c.anyErr:
				if err == nil {
					t.Fatal("Put succeeded, want error")
				}
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("Put error = %v, want %v", err, c.wantErr)
				}
			case err != nil:
				t.Fatalf("Put: %v", err)
			}
			if body != "tarball" {
				t.Errorf("uploaded body = %q, want %q", body, "tarball")
			}
		})
	}
}

func TestClientBearerToken(t *testing.T) {
	var auth string
	ts := blobServer(t, http.StatusNoContent, &auth, nil)

	cl := &Client{BaseURL: ts.URL, Token: "s3cret"}
	if _, err := cl.Stat(t.Context(), testBlobDigest); err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if auth != "Bearer s3cret" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer s3cret")
	}

	cl.Token = ""
	if _, err := cl.Stat(t.Context(), testBlobDigest); err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if auth != "" {
		t.Errorf("Authorization without token = %q, want empty", auth)
	}
}
