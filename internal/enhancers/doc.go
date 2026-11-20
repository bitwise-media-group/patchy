// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package enhancers holds the built-in pkg/enhance implementations:
//
//   - googlecloud, aws and azure resolve a finding's repository from the
//     ownership labels or tags on the cloud resource it was raised against
//     (cloudtags.go is the engine AWS and Azure share). Their Dynamic*
//     wrappers read configuration off the matching Integration per
//     enhancement.
//   - dynamicgeneric fans one enhancement out to every generic Integration
//     with enhance on, attributing each result to its Integration
//     (MultiEnhancer).
//   - staticfile is a YAML-backed repo→ownership map that doubles as the
//     fake CMDB for development and e2e.
//
// A real CMDB integration will implement the same pkg/enhance.Enhancer
// interface.
package enhancers
