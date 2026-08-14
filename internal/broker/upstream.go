// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ParseTarget parses an upstream base URL, requiring an absolute http(s) URL.
func ParseTarget(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("broker: upstream URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("broker: upstream URL %q is not an absolute http(s) URL", raw)
	}
	return u, nil
}

// readKeyFile reads an API key from a mounted Secret file, per request, so a
// rotated Secret propagates without a broker restart.
func readKeyFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read key file: %w", err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return "", fmt.Errorf("key file %s is empty", path)
	}
	return key, nil
}
