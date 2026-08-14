// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// foundryScope is the Entra token scope for Azure AI (Cognitive Services)
// endpoints.
const foundryScope = "https://cognitiveservices.azure.com/.default"

// FoundryKey is the Microsoft Foundry route in API-key mode: inject the key
// from a mounted Secret file, same rotation posture as the Anthropic route.
func FoundryKey(target *url.URL, keyFile string) Upstream {
	return Upstream{
		Target: target,
		Credential: func(_ context.Context, req *http.Request) error {
			key, err := readKeyFile(keyFile)
			if err != nil {
				return err
			}
			req.Header.Set("x-api-key", key)
			return nil
		},
		Ready: func(context.Context) error {
			_, err := readKeyFile(keyFile)
			return err
		},
	}
}

// FoundryEntra is the Foundry route in Entra mode: attach a bearer token from
// the broker's ambient Azure identity (Workload Identity in-cluster), cached
// and refreshed broker-side — which is what fixes a static bearer expiring
// mid-run on long evaluation jobs.
func FoundryEntra(target *url.URL) (Upstream, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return Upstream{}, fmt.Errorf("broker: azure default credential: %w", err)
	}
	ts := &entraTokenSource{cred: cred}
	return Upstream{
		Target: target,
		Credential: func(ctx context.Context, req *http.Request) error {
			tok, err := ts.token(ctx)
			if err != nil {
				return fmt.Errorf("resolve Entra token: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			return nil
		},
		Ready: func(ctx context.Context) error {
			_, err := ts.token(ctx)
			return err
		},
	}, nil
}

// entraTokenSource caches one Entra access token, refreshing it a safety
// margin before expiry.
type entraTokenSource struct {
	cred azcore.TokenCredential

	mu     sync.Mutex
	cached string
	expiry time.Time
}

// refreshMargin is how long before expiry a cached token is refreshed.
const refreshMargin = 5 * time.Minute

func (t *entraTokenSource) token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cached != "" && time.Now().Before(t.expiry.Add(-refreshMargin)) {
		return t.cached, nil
	}
	tok, err := t.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{foundryScope}})
	if err != nil {
		return "", err
	}
	t.cached, t.expiry = tok.Token, tok.ExpiresOn
	return t.cached, nil
}
