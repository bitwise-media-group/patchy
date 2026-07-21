# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# The credential-less dev stand-in for the agent-runner image: a shell script
# named agent-runner that prints canned PATCHY-EVENT lines (see ./agent-runner).
# The same image serves as the Job's init container, so it carries the tools
# the prepare script needs (curl for the artifact fetch, sha256sum/tar from
# busybox, git for the synthetic base commit).
#
#   docker build -t patchy/fake-agent:dev hack/fake-agent
#
# `make dev-colima` builds it automatically when PATCHY_OVERLAY=dev-fake.
FROM alpine:3.22
RUN apk add --no-cache curl git
COPY agent-runner /usr/local/bin/agent-runner
RUN chmod 0755 /usr/local/bin/agent-runner
USER 65532:65532
