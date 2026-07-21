// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package fakegithub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"time"
)

// HeadSHA is the commit every un-pushed branch resolves to — the SHA
// source-controller pins Repositories to (gitdata.go's fixed base).
const HeadSHA = BaseSHA

// tarballRedirect mimics GitHub's archive API: a 302 to the signed download
// URL (here: the fake's own /_tarball path).
func (s *Server) tarballRedirect(w http.ResponseWriter, r *http.Request) {
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	ref := r.PathValue("ref")
	loc := fmt.Sprintf("%s/api/v3/_tarball/%s/%s/%s", s.externalURL, owner, repo, ref)
	http.Redirect(w, r, loc, http.StatusFound)
}

// tarball serves the archive itself: a deterministic tree-only tar.gz with
// GitHub's top-level "<owner>-<repo>-<sha7>/" directory.
func (s *Server) tarball(w http.ResponseWriter, r *http.Request) {
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	top := fmt.Sprintf("%s-%s-%s/", owner, repo, HeadSHA[:7])

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := []struct {
		name, body string
	}{
		{top + "README.md", "# " + owner + "/" + repo + "\n"},
		{top + "app.js", "vulnerable();\n"},
	}
	for _, f := range files {
		_ = tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: 0o644, Size: int64(len(f.body)), ModTime: time.Unix(0, 0),
		})
		_, _ = tw.Write([]byte(f.body))
	}
	_ = tw.Close()
	_ = gz.Close()

	w.Header().Set("Content-Type", "application/gzip")
	_, _ = w.Write(buf.Bytes())
}
