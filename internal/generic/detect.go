// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package generic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
)

// EventFindings is the internal event discriminator for a findings delivery.
// Distinct from the wire value so a handler switch cannot confuse the two
// vocabularies.
const EventFindings = "generic.findings"

// Detect reports the internal event type of a delivery: "ping" for a
// connectivity test (the server answers 204), EventFindings for findings.
// An undecodable body, an unsupported version, or an unknown event is an
// error — the caller answers 400.
func Detect(body []byte) (string, error) {
	var env struct {
		Version string `json:"version"`
		Event   string `json:"event"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("generic: decode delivery: %w", err)
	}
	if env.Version != pkggeneric.Version {
		return "", fmt.Errorf("generic: unsupported contract version %q (want %q)", env.Version, pkggeneric.Version)
	}
	switch env.Event {
	case pkggeneric.EventPing:
		return "ping", nil
	case pkggeneric.EventFindings:
		return EventFindings, nil
	default:
		return "", fmt.Errorf("generic: unknown event %q", env.Event)
	}
}

// DeliveryID derives the dedup key for a delivery: the caller-chosen
// DeliveryHeader when present, else the body hash — byte-identical
// redeliveries dedup, distinct payloads never do (the wiz precedent).
func DeliveryID(r *http.Request, body []byte) string {
	if id := r.Header.Get(pkggeneric.DeliveryHeader); id != "" {
		return id
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])[:16]
}

// NameFromPath extracts the integration name from a concrete request path of
// the form /generic/<name>/webhooks.
func NameFromPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/generic/")
	if !ok {
		return "", false
	}
	name, ok := strings.CutSuffix(rest, "/webhooks")
	if !ok || name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}
