// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"k8s.io/client-go/kubernetes"

	"github.com/bitwise-media-group/patchy/internal/provider"
	"github.com/bitwise-media-group/patchy/internal/tokenauth"
)

// TokenHeader carries the caller's projected ServiceAccount token. It is
// stripped before any byte is forwarded upstream: the token authenticates the
// caller to the broker and must never travel further.
const TokenHeader = provider.BrokerTokenHeader

// podNameExtraKey is where the API server reports the bound pod of a
// pod-bound projected token on the TokenReview user info.
const podNameExtraKey = tokenauth.PodNameExtraKey

// newAuthenticator builds the caching TokenReview authenticator that admits
// only the agent ServiceAccount.
func newAuthenticator(cs kubernetes.Interface, cfg Config) *tokenauth.Authenticator {
	return tokenauth.New(cs, tokenauth.Config{
		Audience: cfg.Audience,
		Subjects: []string{tokenauth.ServiceAccount(cfg.AgentNamespace, cfg.AgentServiceAccount)},
		TTL:      cfg.VerdictTTL,
	})
}
