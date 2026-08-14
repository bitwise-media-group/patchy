// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// emptyPayloadHash is the SHA-256 of an empty body, hex-encoded — SigV4's
// required payload hash for a bodyless request.
const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// bedrockService is the SigV4 service name for the Bedrock runtime API.
const bedrockService = "bedrock"

// Bedrock is the Amazon Bedrock route: SigV4-sign each request with the
// broker's ambient AWS identity (IRSA / EKS Pod Identity / env credentials —
// whatever config.LoadDefaultConfig resolves) and forward. The route buffers
// bodies: SigV4 signs a hash of the payload, so the whole body must be in
// hand before the request leaves.
func Bedrock(ctx context.Context, target *url.URL, region string) (Upstream, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return Upstream{}, fmt.Errorf("broker: load AWS config: %w", err)
	}
	signer := v4.NewSigner()
	return Upstream{
		Target:     target,
		BufferBody: true,
		Credential: func(ctx context.Context, req *http.Request) error {
			creds, err := cfg.Credentials.Retrieve(ctx)
			if err != nil {
				return fmt.Errorf("resolve AWS credentials: %w", err)
			}
			hash, err := payloadHash(req)
			if err != nil {
				return err
			}
			return signer.SignHTTP(ctx, creds, req, hash, bedrockService, region, time.Now().UTC())
		},
		Ready: func(ctx context.Context) error {
			_, err := cfg.Credentials.Retrieve(ctx)
			return err
		},
	}, nil
}

// payloadHash hex-encodes the SHA-256 of the (already buffered) request body.
func payloadHash(req *http.Request) (string, error) {
	if req.GetBody == nil {
		return emptyPayloadHash, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return "", fmt.Errorf("reread request body: %w", err)
	}
	defer func() { _ = body.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, body); err != nil {
		return "", fmt.Errorf("hash request body: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
