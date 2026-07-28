// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package wizapi is a minimal Wiz GraphQL API client, holding exactly what
// the write-back seam needs: an OAuth2 client-credentials token source and
// the updateIssue mutation that rejects an issue with a note. It is the only
// place a Wiz API credential is exercised, mirroring how ghpush is the only
// place the forge write credential is.
package wizapi
