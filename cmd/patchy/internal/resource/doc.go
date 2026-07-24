// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package resource is the noun half of the patchy grammar: it maps what a user
// types (finding, findings, fnd) onto the kind, list type and API resource name
// the command then works with.
//
// The aliases are not invented here. Each one is the shortName the CRD already
// declares (api/v1alpha1, +kubebuilder:resource:shortName), so `patchy get fnd`
// and `kubectl get fnd` always mean the same thing — a user who learns one
// spelling has learned both.
package resource
