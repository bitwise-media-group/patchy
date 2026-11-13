// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package dockerfile lists the images a Dockerfile build pulls — FROM,
// COPY --from, RUN --mount from= and the frontend (# syntax= or
// BUILDKIT_SYNTAX) — expanded the way BuildKit expands them, so the build
// engine can hold them against the config's source policy before buildx
// runs. Parsing and expansion are BuildKit's own (frontend/dockerfile
// parser and shell); nothing here builds or pulls.
package dockerfile
