// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package azureinv reads an Azure resource's tags from Azure Resource Graph.
//
// The tags patchy needs are not reliably in the payload that brought the
// finding. A scanner delivery describes the resource (ARM resource ID, type,
// subscription), but its tags are optional, point-in-time extras, so
// ownership is looked up separately. Unlike AWS there is no backend to
// choose: Resource Graph is free, always on, and tenant-wide. One KQL lookup
// by resource ID answers for every subscription the caller's identity can
// read, optionally narrowed to a management group.
//
// Credentials come from the Azure default chain. In-cluster that means
// Microsoft Entra Workload ID or workload identity federation, so no key
// material exists anywhere. This read-only lookup is the only reason
// context-controller talks to Azure.
//
// The client is one struct with one lookup method, so the enhancer that uses
// it can be tested against a hand-written fake, and neither the enhancer nor
// anything in pkg/ names an Azure SDK type.
package azureinv
