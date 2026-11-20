// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package gcpasset reads a Google Cloud resource's labels through Cloud Asset
// Inventory.
//
// The labels patchy needs are not in the notification that brought the
// finding. A Security Command Center message describes the resource (name,
// type, project) but carries none of its labels, so ownership is looked up
// separately. That read-only lookup is the only reason context-controller
// holds a Google Cloud credential.
//
// The client is one struct with one lookup method, so the enhancer that uses
// it can be tested against a hand-written fake, and neither the enhancer nor
// anything in pkg/ names a Google Cloud type.
package gcpasset
