// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// sealKey derives the 32-byte AES key from the OAuth2 client secret via
// HKDF-SHA256. Deriving instead of storing means no key management beyond
// the secret the flow needs anyway; rotating the secret invalidates every
// outstanding state blob and session, which is the desired failure mode.
func sealKey(clientSecret, info string) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, []byte(clientSecret), nil, info, 32)
	if err != nil {
		return nil, fmt.Errorf("derive %s key: %w", info, err)
	}
	return key, nil
}

// seal encrypts v's JSON with AES-256-GCM and encodes it URL-safely. The
// random GCM nonce is prepended to the ciphertext.
func seal(key []byte, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	aead, err := newAEAD(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("seal nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, plain, nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// unseal decrypts a seal blob into v. Any tampering (or a key rotated since
// sealing) fails authentication and returns an error.
func unseal(key []byte, blob string, v any) error {
	sealed, err := base64.RawURLEncoding.DecodeString(blob)
	if err != nil {
		return fmt.Errorf("unseal decode: %w", err)
	}
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}
	if len(sealed) < aead.NonceSize() {
		return fmt.Errorf("unseal: blob shorter than nonce")
	}
	nonce, ciphertext := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return fmt.Errorf("unseal: %w", err)
	}
	if err := json.Unmarshal(plain, v); err != nil {
		return fmt.Errorf("unseal decode json: %w", err)
	}
	return nil
}

// newAEAD builds the AES-256-GCM cipher for a derived key.
func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("seal cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("seal aead: %w", err)
	}
	return aead, nil
}

// randomToken returns a URL-safe random string for CSRF tokens, nonces, and
// PKCE verifiers.
func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Seal encrypts v's JSON under key (AES-256-GCM, URL-safe): the sealed-blob
// format the session and state cookies use, for other cookie owners.
func Seal(key []byte, v any) (string, error) { return seal(key, v) }

// Unseal decrypts a Seal blob into v; tampering or a rotated key fails.
func Unseal(key []byte, blob string, v any) error { return unseal(key, blob, v) }

// SealKey derives a 32-byte seal key from secret material under an HKDF
// label.
func SealKey(secret, info string) ([]byte, error) { return sealKey(secret, info) }

// RandomToken returns a URL-safe random string (CSRF tokens, PKCE).
func RandomToken() (string, error) { return randomToken() }
