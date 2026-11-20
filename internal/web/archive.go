// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/archive"
)

// eventArchived tells a transcript stream's client that the run's
// conversation lives in the archive: fetch it through a presigned URL
// instead. The server never proxies bucket bytes.
const eventArchived = "archived"

// DefaultPresignTTL is how long a presigned archive URL lives when the
// operator configures none. Short, because the URL is a bearer capability:
// whoever holds it can read the object until it expires.
const DefaultPresignTTL = 5 * time.Minute

// archiveObjects maps the route's object names onto archive objects, and
// whether the browser should download it rather than read it.
var archiveObjects = map[string]struct {
	name     string
	download bool
}{
	"transcript": {v1alpha1.ArchiveObjectTranscript, false},
	"report":     {v1alpha1.ArchiveObjectReport, true},
	"patch":      {v1alpha1.ArchiveObjectPatch, true},
	"manifest":   {v1alpha1.ArchiveObjectManifest, false},
}

// PresignedURL is the archive route's response: a URL the browser GETs
// directly from the bucket, and when it stops working.
type PresignedURL struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
}

// WithArchive lets the server presign archived run objects. Without it the
// archive route answers 404, and archived transcripts cannot be played back.
func (s *Server) WithArchive(store archive.Store, ttl time.Duration) *Server {
	if ttl <= 0 {
		ttl = DefaultPresignTTL
	}
	s.archive, s.presignTTL = store, ttl
	return s
}

// handleArchive presigns one archived object of one run. It is gated and
// cross-checked exactly like the transcript stream (the same view grant, the
// same findingRef check), and every URL it hands out is audit-logged, since
// the URL itself carries no identity once it leaves.
func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	v, ok := s.authorize(w, r)
	if !ok {
		return
	}
	finding := r.PathValue("name")
	kind := v1alpha1.RunKind(r.PathValue("kind"))
	attempt, err := strconv.ParseInt(r.PathValue("attempt"), 10, 32)
	if err != nil || attempt < 1 {
		http.Error(w, "invalid attempt", http.StatusBadRequest)
		return
	}
	want, ok := archiveObjects[r.PathValue("object")]
	if !ok {
		http.Error(w, "unknown archive object", http.StatusNotFound)
		return
	}
	run, err := s.findRun(r.Context(), finding, kind, int32(attempt))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	obj, ok := run.archive.Object(want.name)
	if !ok {
		http.Error(w, "run has no such archived object", http.StatusNotFound)
		return
	}
	if s.archive == nil {
		http.Error(w, "archive access is not configured", http.StatusNotFound)
		return
	}
	attachment := ""
	if want.download {
		attachment = run.name + "-" + want.name
	}
	url, exp, err := s.archive.Presign(r.Context(), obj.Key, s.presignTTL, attachment)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "presign archive object",
			slog.String("run", run.name), slog.String("object", want.name), slog.Any("error", err))
		http.Error(w, "presign failed", http.StatusBadGateway)
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "archive object presigned",
		slog.String("user", v.id.Username), slog.String("finding", finding),
		slog.String("run", run.name), slog.String("object", want.name),
		slog.String("key", obj.Key), slog.Time("expires", exp))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(PresignedURL{URL: url, ExpiresAt: exp.UTC().Format(time.RFC3339)})
}

// writeArchived tells the client to play the transcript back from the
// archive, then ends the stream.
func writeArchived(w http.ResponseWriter, flusher http.Flusher) {
	_, _ = w.Write([]byte("event: " + eventArchived + "\ndata: {}\n\n"))
	writeEnd(w, flusher)
}
