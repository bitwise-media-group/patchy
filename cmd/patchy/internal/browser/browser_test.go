// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package browser

import "testing"

// TestOpenRejectsNonHTTP covers the scheme check. These URLs come from CR
// status, but a file:// or javascript: value reaching the platform's URL
// handler is not something to find out about in production.
func TestOpenRejectsNonHTTP(t *testing.T) {
	for _, url := range []string{
		"",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"ftp://example.test",
		"example.test",
		" https://example.test",
	} {
		if err := Open(url); err == nil {
			t.Errorf("Open(%q) was accepted", url)
		}
	}
}
