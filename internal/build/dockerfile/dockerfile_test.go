// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package dockerfile

import (
	"slices"
	"testing"
)

func TestSources(t *testing.T) {
	tests := []struct {
		name       string
		dockerfile string
		args       map[string]string
		platforms  []string
		want       []string // instruction|ref, or instruction|!raw for an unresolvable one
	}{
		{
			name:       "plain from",
			dockerfile: "FROM alpine:3.20\nRUN true\n",
			want:       []string{"FROM|alpine:3.20"},
		},
		{
			name: "stages, scratch and index references are skipped",
			dockerfile: "FROM golang:1.27 AS Build\n" +
				"RUN go build\n" +
				"FROM build AS test\n" +
				"FROM scratch\n" +
				"COPY --from=build /out /out\n" +
				"COPY --from=0 /x /x\n" +
				"COPY --from=ghcr.io/acme/tools:v1 /bin/tool /bin/tool\n",
			want: []string{"FROM|golang:1.27", "COPY --from|ghcr.io/acme/tools:v1"},
		},
		{
			name: "global args with defaults and build-arg overrides",
			dockerfile: "ARG REGISTRY=docker.io\n" +
				"ARG BASE=library/debian\n" +
				"ARG TAG\n" +
				"FROM ${REGISTRY}/${BASE}:${TAG:-12}\n",
			args: map[string]string{"BASE": "acme/debian"},
			want: []string{"FROM|docker.io/acme/debian:12"},
		},
		{
			name:       "unset arg leaving an empty component is unresolvable",
			dockerfile: "ARG REGISTRY\nFROM ${REGISTRY}/app:v1\n",
			want:       []string{"FROM|!${REGISTRY}/app:v1"},
		},
		{
			name:       "platform args expand per platform",
			dockerfile: "FROM --platform=$BUILDPLATFORM ghcr.io/acme/base-${TARGETARCH}:v1\n",
			platforms:  []string{"linux/amd64", "linux/arm64"},
			want:       []string{"FROM|ghcr.io/acme/base-amd64:v1", "FROM|ghcr.io/acme/base-arm64:v1"},
		},
		{
			name: "run mount from image and stage, stage args",
			dockerfile: "FROM alpine AS deps\n" +
				"FROM alpine\n" +
				"ARG CACHE_IMAGE=ghcr.io/acme/cache:v2\n" +
				"RUN --mount=type=bind,from=deps,target=/deps true\n" +
				"RUN --mount=type=cache,target=/c,from=${CACHE_IMAGE} true\n" +
				"RUN --mount=type=secret,id=x true\n",
			want: []string{"FROM|alpine", "RUN --mount from|ghcr.io/acme/cache:v2"},
		},
		{
			name: "stage env expands flags",
			dockerfile: "FROM alpine\n" +
				"ENV TOOLS=ghcr.io/acme/tools:v3\n" +
				"COPY --from=$TOOLS /t /t\n",
			want: []string{"FROM|alpine", "COPY --from|ghcr.io/acme/tools:v3"},
		},
		{
			name:       "global arg needs redeclaring inside a stage",
			dockerfile: "ARG TOOLS=ghcr.io/acme/tools:v1\nFROM alpine\nARG TOOLS\nCOPY --from=${TOOLS} /t /t\n",
			want:       []string{"FROM|alpine", "COPY --from|ghcr.io/acme/tools:v1"},
		},
		{
			name:       "syntax directive",
			dockerfile: "# syntax=docker/dockerfile:1.10\nFROM alpine\n",
			want:       []string{"syntax|docker/dockerfile:1.10", "FROM|alpine"},
		},
		{
			name:       "BUILDKIT_SYNTAX overrides the directive",
			dockerfile: "# syntax=docker/dockerfile:1.10\nFROM alpine\n",
			args:       map[string]string{"BUILDKIT_SYNTAX": "registry.example.com/dockerfile:1"},
			want:       []string{"syntax|registry.example.com/dockerfile:1", "FROM|alpine"},
		},
		{
			name: "line continuations and heredoc bodies",
			dockerfile: "FROM \\\n  ghcr.io/acme/base:v1\n" +
				"RUN <<EOT\n" +
				"FROM evil.example.com/not-an-instruction\n" +
				"EOT\n",
			want: []string{"FROM|ghcr.io/acme/base:v1"},
		},
		{
			name:       "escape directive",
			dockerfile: "# escape=`\nFROM `\n  ghcr.io/acme/win:v1\n",
			want:       []string{"FROM|ghcr.io/acme/win:v1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Sources(Input{Content: []byte(tt.dockerfile), Args: tt.args, Platforms: tt.platforms})
			if err != nil {
				t.Fatalf("Sources: %v", err)
			}
			var flat []string
			for _, s := range got {
				if s.Err != "" {
					flat = append(flat, s.Instruction+"|!"+s.Raw)
					continue
				}
				flat = append(flat, s.Instruction+"|"+s.Ref)
			}
			if !slices.Equal(flat, tt.want) {
				t.Errorf("Sources = %q, want %q", flat, tt.want)
			}
		})
	}
}
