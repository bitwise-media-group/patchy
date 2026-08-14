// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"context"
	"net/http"
	"net/url"
)

// Anthropic is the first-party route: inject the credential from a mounted
// Secret file and forward otherwise unchanged — the anthropic-version and
// anthropic-beta headers the CLI sends pass through, so prompt caching and
// beta features survive the hop. An API key rides x-api-key; bearer=true
// sends Authorization: Bearer instead, which is how a `claude setup-token`
// OAuth token authenticates. Either way the file is read per request, so a
// rotated Secret propagates without a restart.
func Anthropic(target *url.URL, keyFile string, bearer bool) Upstream {
	return Upstream{
		Target: target,
		Credential: func(_ context.Context, req *http.Request) error {
			key, err := readKeyFile(keyFile)
			if err != nil {
				return err
			}
			if bearer {
				req.Header.Set("Authorization", "Bearer "+key)
			} else {
				req.Header.Set("x-api-key", key)
			}
			return nil
		},
		Ready: func(context.Context) error {
			_, err := readKeyFile(keyFile)
			return err
		},
	}
}
