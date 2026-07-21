// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command fakegithub runs the e2e suite's in-memory GitHub API as a
// standalone server — the credential-less GitHub stand-in for local dev.
// The dev-fake overlay runs it in-cluster (hack/fakegithub/Dockerfile,
// deploy/kustomize/overlays/dev-fake/fakegithub.yaml) with the
// Integration/Forge baseURL pointed at its Service, and the whole pipeline
// runs against this instead of github.com: alert fetches, issue projection,
// repository tarballs, the Git Data push, and pull requests. `mise run
// fakegithub` runs the same server on the host for poking at it directly.
//
// State is in-memory and per-process: restart it (in-cluster,
// `kubectl -n patchy rollout restart deployment patchy-fakegithub`) and the
// slate is clean.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
)

func main() {
	addr := flag.String("addr", ":9990", "listen address")
	externalURL := flag.String("external-url", "http://192.168.5.2:9990",
		"URL clients reach this server at (stamped into tarball redirects); "+
			"the default is the host as seen from a colima/lima guest, for "+
			"host-run mode — the in-cluster Deployment overrides it with its "+
			"Service DNS name")
	flag.Parse()

	_, handler := fakegithub.NewStandalone(*externalURL)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(handler),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("fakegithub listening on %s (external URL %s)\n", *addr, *externalURL)
	fmt.Println("point Integration/Forge spec baseURL at the external URL — see deploy/kustomize/overlays/dev-fake")
	log.Fatal(srv.ListenAndServe())
}

// logRequests prints one line per API call so the pipeline's GitHub traffic
// is visible in the terminal.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
