// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package archive is the cloud seam for long-term archival of agent runs: a
// create-only object store over Amazon S3 or Google Cloud Storage.
//
// Patchy only ever writes an object once and never deletes one. Retention,
// legal hold, encryption keys, tiering and expiry all belong to the bucket's
// own policy (S3 Object Lock, a GCS retention lock, CMK/CMEK, lifecycle
// rules), so an operator's audit guarantees never depend on patchy behaving.
//
// Every write is conditional on the object not existing (S3 If-None-Match: *,
// GCS DoesNotExist). A retry that finds an object already there succeeds when
// the stored sha256 matches the new body and fails with ErrConflict when it
// does not, so a re-collected run converges instead of either overwriting
// evidence or erroring forever.
//
// Credentials always come from the platform's default chain (IRSA or EKS Pod
// Identity, GKE workload identity, ADC), so no key material exists anywhere.
// The package has no Kubernetes imports: internal/runarchive owns assembling a
// run's objects and recording where they went.
package archive
