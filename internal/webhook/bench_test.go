// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Tier-1 microbenchmarks for the webhook receiver's per-delivery costs: HMAC
// validation across payload sizes and candidate-secret counts, and the
// delivery dedup ring at capacity. Both sit in front of every ingested alert.

package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

// benchBody is a deterministic pseudo-JSON payload of n bytes.
func benchBody(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte('a' + i%26)
	}
	return out
}

// BenchmarkHMACAuthenticate measures validating one delivery signature. The
// signature is computed with the LAST candidate secret, so the k>1 cases pay
// for scanning the whole candidate set — the receiver's worst case.
func BenchmarkHMACAuthenticate(b *testing.B) {
	sizes := map[string]int{"4KB": 4 << 10, "1MB": 1 << 20, "25MB": 25 << 20}
	for _, name := range []string{"4KB", "1MB", "25MB"} {
		body := benchBody(sizes[name])
		for _, k := range []int{1, 8} {
			secrets := make([][]byte, 0, k)
			for s := range k {
				secrets = append(secrets, fmt.Appendf(nil, "bench-secret-%d", s))
			}
			mac := hmac.New(sha256.New, secrets[k-1])
			mac.Write(body)
			sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
			auth := &HMACAuthenticator{SecretsFor: func(context.Context) [][]byte { return secrets }}
			req := httptest.NewRequest("POST", "/github/webhooks", nil)
			req.Header.Set("X-Hub-Signature-256", sig)
			b.Run(fmt.Sprintf("size=%s/secrets=%d", name, k), func(b *testing.B) {
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				for b.Loop() {
					if err := auth.Authenticate(b.Context(), req, body); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkDedup measures recording a fresh delivery ID against a full
// 1024-entry ring — every add evicts, the steady state of a busy receiver.
func BenchmarkDedup(b *testing.B) {
	d := newDedup(1024, 5*time.Minute)
	d.now = func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }
	for i := range 1024 {
		d.add(fmt.Sprintf("/github/webhooks|seed-%d", i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if !d.add(fmt.Sprintf("/github/webhooks|bench-%d", i)) {
			b.Fatal("fresh id reported as duplicate")
		}
	}
}
