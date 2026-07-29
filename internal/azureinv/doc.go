// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package azureinv reads an Azure resource's tags from Azure Resource Graph.
//
// It exists because the tags patchy needs are not reliably in the payload
// that brought the finding: a scanner delivery describes the resource (ARM
// resource ID, type, subscription) but its tags are optional, point-in-time
// extras, so ownership has to be looked up separately. Unlike AWS there is no
// backend to choose: Resource Graph is free, always on, and tenant-wide — one
// KQL lookup by resource ID answers for every subscription the caller's
// identity can read, optionally narrowed to a management group.
//
// Credentials come from the Azure default chain — in-cluster that means
// Microsoft Entra Workload ID or workload identity federation, so no key
// material exists anywhere — and the lookup is the only reason
// context-controller talks to Azure. It is read-only.
//
// The client is deliberately thin — one method, one struct — so the enhancer
// that uses it can be tested against a hand-written fake and neither the
// enhancer nor anything in pkg/ ever names an Azure SDK type.
package azureinv
