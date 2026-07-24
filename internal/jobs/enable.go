// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ResolveRunners returns the harness ids that are enabled given the configured
// runner fleet and the credentials present in the agent namespace. A runner is
// enabled when its credential Secret exists and carries the configured key; the
// fake runner (no Secret) is always enabled when configured. Enumeration is by
// get-by-name only — the job controllers hold no list/watch on Secrets, by
// design — so this probes each configured runner's Secret individually.
//
// restrict, when non-empty, pins the enabled set to exactly those harness ids
// (still verifying each is a configured runner with a usable credential, so an
// operator who names a harness gets a clear error rather than a silently
// dropped one). When empty, the enabled set is every configured runner whose
// credential is present — the "any harness with credentials" default.
func ResolveRunners(ctx context.Context, cs kubernetes.Interface, namespace string,
	runners map[string]Runner, restrict []string) ([]string, error) {
	candidates := slices.Sorted(maps.Keys(runners))
	explicit := len(restrict) > 0
	if explicit {
		candidates = restrict
	}

	var enabled []string
	var errs []error
	for _, id := range candidates {
		r, ok := runners[id]
		if !ok {
			errs = append(errs, fmt.Errorf("harness %q is enabled but has no runner image configured", id))
			continue
		}
		ok, err := credentialPresent(ctx, cs, namespace, r)
		if err != nil {
			errs = append(errs, fmt.Errorf("harness %q: %w", id, err))
			continue
		}
		if !ok {
			if explicit {
				errs = append(errs, fmt.Errorf("harness %q is enabled but its credential Secret %q is missing in namespace %q",
					id, r.Secret, namespace))
			}
			continue
		}
		enabled = append(enabled, id)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	slices.Sort(enabled)
	return enabled, nil
}

// credentialPresent reports whether a runner's model credential is usable: the
// fake runner needs none; otherwise the Secret must exist and carry the
// configured key. A missing Secret is not an error (the caller decides whether
// absence disables the harness or fails startup); a Secret present but missing
// its key is always an error — a positive misconfiguration.
func credentialPresent(ctx context.Context, cs kubernetes.Interface, namespace string, r Runner) (bool, error) {
	if r.Secret == "" {
		return true, nil // fake runner
	}
	sec, err := cs.CoreV1().Secrets(namespace).Get(ctx, r.Secret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get credential Secret %q: %w", r.Secret, err)
	}
	if _, ok := sec.Data[r.SecretKey]; !ok {
		return false, fmt.Errorf("credential Secret %q has no key %q", r.Secret, r.SecretKey)
	}
	return true, nil
}
