// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package yamledit performs surgical edits on YAML files that humans and
// other tools also write: it uses the YAML AST only to LOCATE a scalar,
// then byte-splices exactly that token, never re-encoding the document.
// Comments, ordering, indentation, quoting style and every other byte
// outside the replaced token survive verbatim — the files it edits (chart
// manifests, discovery values) live in git and their diffs are reviewed,
// so a formatting round-trip would be noise at best and a byte-identity
// gate failure at worst.
//
// Paths are the yq subset the mirror needs: `.a.b`, `.a."k.ey"`, `.a[0]`,
// composable as in `.template.spec.containers[0].image`. Only single-line
// scalar targets are supported; the caller states the value it expects to
// replace, and a mismatch aborts the edit rather than clobbering something
// unexpected.
package yamledit
