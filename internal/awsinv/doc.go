// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package awsinv reads an AWS resource's tags from an organization-level
// inventory.
//
// The tags patchy needs are not reliably in the payload that brought the
// finding. A scanner delivery describes the resource (ARN, type, account),
// but its tags are optional, point-in-time extras, so ownership is looked up
// separately. Two inventories can answer, and which one an estate has is a
// deployment fact: AWS Config with an organization aggregator (common in
// large enterprises, where Config already runs estate-wide for compliance)
// or an organization-wide AWS Resource Explorer view (free, and available
// without Config). Exactly one is configured. Both answer the same question,
// so nothing downstream can tell them apart.
//
// Credentials come from the SDK default chain. In-cluster that means EKS Pod
// Identity, IRSA, or web-identity federation, so no key material exists
// anywhere. This read-only lookup is the only reason context-controller
// talks to AWS.
//
// The client is one struct with one lookup method, so the enhancer that uses
// it can be tested against a hand-written fake, and neither the enhancer nor
// anything in pkg/ names an AWS SDK type.
package awsinv
