# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT

# The fake GitHub image the dev-fake overlay runs in-cluster: the e2e suite's
# in-memory GitHub API (e2e/fakegithub/cmd) behind the same distroless-static
# runtime as the controllers. Like Dockerfile.controller, no Go toolchain in
# here — the binary is cross-compiled on the host and handed in via the build
# context (bin/):
#
#   (cd e2e && CGO_ENABLED=0 GOOS=linux GOARCH=<arch> \
#       go build -trimpath -o ../bin/fakegithub ./fakegithub/cmd)
#   docker build -t patchy/fakegithub:dev -f hack/fakegithub/Dockerfile bin
#
# `make dev-colima` does both automatically when PATCHY_OVERLAY=dev-fake.

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99

COPY fakegithub /usr/local/bin/fakegithub

USER 65532:65532
EXPOSE 9990
ENTRYPOINT ["/usr/local/bin/fakegithub"]
