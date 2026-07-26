// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package gcpasset reads a Google Cloud resource's labels through Cloud Asset
// Inventory.
//
// It exists because the labels patchy needs are not in the notification that
// brought the finding: a Security Command Center message describes the
// resource (name, type, project) but carries none of its labels, so ownership
// has to be looked up separately. That lookup is the only reason
// context-controller holds a cloud credential, and it is read-only.
//
// The client is deliberately thin — one method, one struct — so the enhancer
// that uses it can be tested against a hand-written fake and neither the
// enhancer nor anything in pkg/ ever names a Google Cloud type.
package gcpasset
