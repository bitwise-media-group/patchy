// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"log/slog"
	"net/http"
	"time"
)

// auditWriter counts what one response wrote so the audit line can report
// status and bytes. It exposes the wrapped writer via Unwrap so the proxy's
// ResponseController still reaches the real Flusher.
type auditWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *auditWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *auditWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *auditWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// audit emits the one slog line every request gets: caller pod, route,
// method, path, status, duration, bytes. Never bodies, never headers — model
// prompts and completions must not land in the broker's logs.
func audit(log *slog.Logger, r *http.Request, pod, route string, status int, bytes int64, elapsed time.Duration) {
	log.LogAttrs(r.Context(), slog.LevelInfo, "proxied",
		slog.String("pod", pod),
		slog.String("route", route),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", status),
		slog.Duration("duration", elapsed),
		slog.Int64("bytes", bytes))
}
