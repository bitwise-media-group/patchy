// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/bitwise-media-group/patchy/internal/action"
)

// Exit codes. A CLI that only ever returns 1 forces scripts to grep stderr, so
// the cases a caller might reasonably branch on get their own code.
const (
	// ExitOK reports success.
	ExitOK = 0
	// ExitError reports a runtime failure: unreachable cluster, bad response.
	ExitError = 1
	// ExitUsage reports a malformed invocation — cobra's own errors land here.
	ExitUsage = 2
	// ExitNotFound reports that a named resource does not exist.
	ExitNotFound = 3
	// ExitDenied reports that RBAC refused the action, or that the finding's
	// current phase has no use for it. Both mean "you got nothing done", which
	// is the distinction a script cares about.
	ExitDenied = 4
)

// usageError marks an error as the caller's mistake rather than a failure, so
// Execute can print usage and return ExitUsage.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// errUsage wraps err as a usage error.
func errUsage(err error) error { return &usageError{err: err} }

// exitCode classifies an error from a command into a process exit code.
func exitCode(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case isUsage(err):
		return ExitUsage
	case apierrors.IsNotFound(err):
		return ExitNotFound
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err),
		errors.Is(err, action.ErrUnavailable),
		errors.Is(err, errDenied):
		return ExitDenied
	default:
		return ExitError
	}
}

// isUsage reports whether err is a usage mistake.
func isUsage(err error) bool {
	var ue *usageError
	return errors.As(err, &ue)
}

// errDenied marks a refusal the CLI decided locally — an access review that
// came back no — as distinct from one the API server returned.
var errDenied = errors.New("permission denied")
