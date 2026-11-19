// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// OpenAI is the codex first-party route: inject the platform API key from a
// mounted Secret file as Authorization: Bearer and forward otherwise
// unchanged. The file is read per request, so a rotated Secret propagates
// without a restart — the same posture as the Anthropic route.
func OpenAI(target *url.URL, keyFile string) Upstream {
	return Upstream{
		Target: target,
		Credential: func(_ context.Context, req *http.Request) error {
			key, err := readKeyFile(keyFile)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+key)
			return nil
		},
		Ready: func(context.Context) error {
			_, err := readKeyFile(keyFile)
			return err
		},
	}
}

// AzureOpenAIKey is the Azure OpenAI route in API-key mode: inject the key
// from a mounted Secret file as the api-key header Azure expects.
func AzureOpenAIKey(target *url.URL, keyFile string) Upstream {
	return Upstream{
		Target: target,
		Credential: func(_ context.Context, req *http.Request) error {
			key, err := readKeyFile(keyFile)
			if err != nil {
				return err
			}
			req.Header.Set("api-key", key)
			return nil
		},
		Ready: func(context.Context) error {
			_, err := readKeyFile(keyFile)
			return err
		},
	}
}

// AzureOpenAIEntra is the Azure OpenAI route in Entra mode: attach a bearer
// token from the broker's ambient Azure identity (Workload Identity
// in-cluster), cached and refreshed broker-side — the same Cognitive
// Services scope and token source the Foundry Entra route uses.
func AzureOpenAIEntra(target *url.URL) (Upstream, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return Upstream{}, fmt.Errorf("broker: azure default credential: %w", err)
	}
	return entraUpstream(target, &entraTokenSource{cred: cred}), nil
}
