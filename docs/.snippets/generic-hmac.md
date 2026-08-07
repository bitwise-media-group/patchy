```text
X-Patchy-Signature-256: sha256=<hex of HMAC-SHA256(secret, body)>
```

The signature is computed over the **raw request body**, keyed with the shared secret under the credential Secret's
`webhookSecret` key; verify with a constant-time comparison over the exact bytes received. Signing in Go:

```go
mac := hmac.New(sha256.New, secret)
mac.Write(body)
sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
```
