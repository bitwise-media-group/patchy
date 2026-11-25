// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package repoaccess is the wire contract of the integration-controller's
// internal repository-access endpoint, and the client the status server
// calls it with.
//
// The status server holds no forge credential, so it cannot ask GitHub
// whether patchy can read a repository's alerts. The integration-controller
// can: it holds the Integration credentials already. It answers
// POST /internal/v1/repository-access on a separate in-cluster listener,
// authenticating the caller by an audience-bound projected ServiceAccount
// token (internal/tokenauth). The answer, per repository and Integration,
// is whether each enabled alert family is readable, plus whether a Forge
// covers the repository with write access for remediation.
package repoaccess
