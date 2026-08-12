// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evalapi

import (
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"

	"github.com/bitwise-media-group/patchy/internal/artifact"
)

// validDigest reports whether s is a well-formed sha256 hex digest.
func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// handleWorkspaceHead answers the dedupe probe: 204 cached, 404 absent.
// Uploading is the create privilege, so probing rides the same verb.
func (s *Server) handleWorkspaceHead(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r, VerbCreate); !ok {
		return
	}
	digest := r.PathValue("digest")
	if !validDigest(digest) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	cached, err := s.workspaces.Stat(r.Context(), digest)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "stat workspace",
			slog.String("digest", digest), slog.Any("error", err))
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if !cached {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleWorkspacePut streams a gzip tarball through to source-controller,
// which verifies the digest: 201 stored, 200 already cached, 422 mismatch,
// 413 over the cap.
func (s *Server) handleWorkspacePut(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r, VerbCreate); !ok {
		return
	}
	digest := r.PathValue("digest")
	if !validDigest(digest) {
		writeError(w, http.StatusBadRequest, "malformed workspace digest")
		return
	}
	cached, err := s.workspaces.Stat(r.Context(), digest)
	if err == nil && cached {
		w.WriteHeader(http.StatusOK)
		return
	}

	body := http.MaxBytesReader(w, r.Body, s.limits.MaxWorkspaceBytes)
	err = s.workspaces.Put(r.Context(), digest, body, r.ContentLength)
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxErr):
		writeError(w, http.StatusRequestEntityTooLarge, "workspace exceeds the configured cap")
	case errors.Is(err, artifact.ErrBlobTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "workspace exceeds the configured cap")
	case errors.Is(err, artifact.ErrDigestMismatch):
		writeError(w, http.StatusUnprocessableEntity, "uploaded bytes do not match the digest")
	case err != nil:
		s.log.LogAttrs(r.Context(), slog.LevelError, "store workspace",
			slog.String("digest", digest), slog.Any("error", err))
		writeError(w, http.StatusBadGateway, "storing the workspace failed")
	default:
		w.WriteHeader(http.StatusCreated)
	}
}
