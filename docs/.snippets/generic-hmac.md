```text
X-Patchy-Signature-256: sha256=<hex of HMAC-SHA256(secret, body)>
```

The signature is computed over the **raw request body**. Its key is the shared secret stored under the credential
Secret's `webhookSecret` key. To verify it, use a constant-time comparison over the bytes you received, unchanged.
Signing in Go:

```go
mac := hmac.New(sha256.New, secret)
mac.Write(body)
sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
```
