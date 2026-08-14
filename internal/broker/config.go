// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Default knob values, applied by Config.withDefaults.
const (
	// DefaultAudience is the projected-token audience callers must present.
	DefaultAudience = "patchy-egress-broker"
	// DefaultVerdictTTL bounds TokenReview QPS: one review per caller token
	// per TTL, not one per request.
	DefaultVerdictTTL = time.Minute
	// DefaultPingInterval is the SSE idle keep-alive period.
	DefaultPingInterval = 30 * time.Second
	// DefaultMaxRequestBytes caps a buffered (SigV4-signed) request body.
	DefaultMaxRequestBytes = 10 << 20
)

// CredentialFunc attaches the upstream credential to one outbound request:
// inject an API key header, attach an OAuth bearer, or SigV4-sign. It runs on
// the rewritten outbound request, after the caller's broker token and any
// inbound authorization headers have been stripped.
type CredentialFunc func(ctx context.Context, req *http.Request) error

// Upstream is one brokered route: where it forwards and how it credentials.
type Upstream struct {
	// Target is the upstream base URL the route prefix maps onto.
	Target *url.URL
	// Credential attaches the upstream credential; nil forwards unmodified.
	Credential CredentialFunc
	// BufferBody buffers the request body in memory (bounded by
	// Config.MaxRequestBytes) before forwarding — required when Credential
	// hashes the payload, as SigV4 does.
	BufferBody bool
	// Ready reports whether the route's credential source is usable; readyz
	// fails while any configured route's is not. Nil means always ready.
	Ready func(ctx context.Context) error
}

// Config configures the broker engine.
type Config struct {
	// Audience is the projected-token audience callers must present.
	Audience string
	// AgentNamespace/AgentServiceAccount pin the only identity the broker
	// answers to: system:serviceaccount:<AgentNamespace>:<AgentServiceAccount>.
	AgentNamespace      string
	AgentServiceAccount string
	// VerdictTTL is how long one token's TokenReview verdict is cached.
	VerdictTTL time.Duration
	// PingInterval is the SSE idle keep-alive period; 0 takes the default,
	// negative disables ping injection (pure passthrough).
	PingInterval time.Duration
	// MaxRequestBytes caps a buffered request body (BufferBody routes).
	MaxRequestBytes int64
	// Upstreams is the route table, keyed by path prefix
	// ("anthropic"/"bedrock"/"vertex"/"foundry").
	Upstreams map[string]Upstream
}

// withDefaults returns cfg with zero knobs defaulted.
func (c Config) withDefaults() Config {
	if c.Audience == "" {
		c.Audience = DefaultAudience
	}
	if c.VerdictTTL <= 0 {
		c.VerdictTTL = DefaultVerdictTTL
	}
	if c.PingInterval == 0 {
		c.PingInterval = DefaultPingInterval
	}
	if c.MaxRequestBytes <= 0 {
		c.MaxRequestBytes = DefaultMaxRequestBytes
	}
	return c
}

// validate rejects a config the engine cannot serve.
func (c Config) validate() error {
	if len(c.Upstreams) == 0 {
		return errors.New("broker: no upstream configured")
	}
	if c.AgentNamespace == "" || c.AgentServiceAccount == "" {
		return errors.New("broker: agent namespace and service account are required")
	}
	for name, u := range c.Upstreams {
		if u.Target == nil {
			return fmt.Errorf("broker: upstream %q has no target URL", name)
		}
	}
	return nil
}
