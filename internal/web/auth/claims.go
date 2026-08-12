// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package auth

import (
	"fmt"
)

// MapClaims derives the identity from verified ID-token claims using the
// configured claim names — the one claims-to-identity mapping shared by the
// status server's session flow and the evaluation API's bearer flow, so the
// two can never disagree about who a token names.
func MapClaims(claims map[string]any, cfg ClaimsConfig) (*Identity, error) {
	return mapClaims(claims, cfg)
}

// mapClaims derives the identity from verified ID-token claims using the
// configured claim names. The username claim is required — an identity that
// cannot be named cannot be access-reviewed; groups and display name are
// best-effort.
func mapClaims(claims map[string]any, cfg ClaimsConfig) (*Identity, error) {
	username, _ := claims[cfg.Username].(string)
	if username == "" {
		return nil, fmt.Errorf("token has no usable %q claim", cfg.Username)
	}
	id := &Identity{Username: username, Session: true}
	if name, _ := claims[cfg.DisplayName].(string); name != "" {
		id.DisplayName = name
	}
	switch groups := claims[cfg.Groups].(type) {
	case []any:
		for _, g := range groups {
			if s, ok := g.(string); ok && s != "" {
				id.Groups = append(id.Groups, s)
			}
		}
	case string:
		if groups != "" {
			id.Groups = []string{groups}
		}
	}
	return id, nil
}
