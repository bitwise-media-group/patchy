// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"errors"
	"net/http"

	"github.com/google/go-github/v89/github"
)

// IsNotFound reports whether err (anywhere in its chain) is a GitHub API
// 404 — the projected object no longer exists on the forge.
func IsNotFound(err error) bool {
	var ger *github.ErrorResponse
	return errors.As(err, &ger) && ger.Response != nil && ger.Response.StatusCode == http.StatusNotFound
}
