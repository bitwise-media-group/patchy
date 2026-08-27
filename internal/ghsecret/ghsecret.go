// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package ghsecret turns credential Secrets referenced by Forge and
// Integration resources into GitHub clients. One Secret shape serves both
// kinds: a PAT under "token" (dev), or a GitHub App under "appID" +
// "privateKey" (production), with "webhookSecret" carrying the receiver HMAC
// secret where relevant and "proxyUsername" + "proxyPassword" carrying the
// forward proxy's basic-auth credentials when the resource sets spec.proxy.
// Apps are memoized per Secret resourceVersion so rotation transparently
// rebuilds while steady state costs nothing.
package ghsecret

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"

	"github.com/bitwise-media-group/patchy/internal/ghclient"
)

// Secret keys.
const (
	// KeyToken is a PAT — the dev fallback; production uses App auth.
	KeyToken = "token"
	// KeyAppID is the GitHub App ID (decimal string).
	KeyAppID = "appID"
	// KeyPrivateKey is the App's PEM-encoded RSA private key.
	KeyPrivateKey = "privateKey"
	// KeyWebhookSecret is the receiver HMAC secret.
	KeyWebhookSecret = "webhookSecret"
	// KeyProxyUsername is the forward proxy's basic-auth username.
	KeyProxyUsername = "proxyUsername"
	// KeyProxyPassword is the forward proxy's basic-auth password.
	KeyProxyPassword = "proxyPassword"
)

// ProxyURL composes the effective proxy URL: the resource's spec proxy URL
// with the secret's basic-auth credentials attached, when present. The CRD
// pattern rejects userinfo in the spec URL, so credentials never live
// anywhere but the Secret. rawURL "" means no proxy and returns "".
func ProxyURL(secret *corev1.Secret, rawURL string) (string, error) {
	if rawURL == "" {
		return "", nil
	}
	user := strings.TrimSpace(string(secret.Data[KeyProxyUsername]))
	if user == "" {
		return rawURL, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("proxy url: %w", err)
	}
	u.User = url.UserPassword(user, strings.TrimSpace(string(secret.Data[KeyProxyPassword])))
	return u.String(), nil
}

// Token returns the PAT held in the secret, if any.
func Token(secret *corev1.Secret) (string, bool) {
	tok := strings.TrimSpace(string(secret.Data[KeyToken]))
	return tok, tok != ""
}

// Apps memoizes ghclient.App instances per credential Secret.
type Apps struct {
	mu   sync.Mutex
	apps map[string]*ghclient.App
}

// NewApps builds an empty memo.
func NewApps() *Apps {
	return &Apps{apps: make(map[string]*ghclient.App)}
}

// FromSecret builds (or returns the memoized) App from the secret's
// appID/privateKey keys. baseURL points at GHES; empty means github.com.
// proxy is the referencing resource's raw spec proxy URL ("" = none); its
// basic-auth credentials are read from the secret's proxy keys.
func (a *Apps) FromSecret(secret *corev1.Secret, baseURL, proxy string) (*ghclient.App, error) {
	// The key discriminates on everything an App is built from: the Secret
	// version plus the referencing resource's baseURL and proxy, so two CRs
	// sharing one Secret with different settings each get their own App.
	versionPrefix := secret.Namespace + "/" + secret.Name + "/" + secret.ResourceVersion + "\x00"
	cacheKey := versionPrefix + baseURL + "\x00" + proxy
	a.mu.Lock()
	defer a.mu.Unlock()
	if app, ok := a.apps[cacheKey]; ok {
		return app, nil
	}
	rawID := strings.TrimSpace(string(secret.Data[KeyAppID]))
	if rawID == "" {
		return nil, fmt.Errorf("secret %s/%s has neither %s nor %s",
			secret.Namespace, secret.Name, KeyToken, KeyAppID)
	}
	appID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("secret key %s: %w", KeyAppID, err)
	}
	key := secret.Data[KeyPrivateKey]
	if len(key) == 0 {
		return nil, fmt.Errorf("secret %s/%s missing key %s", secret.Namespace, secret.Name, KeyPrivateKey)
	}
	proxyURL, err := ProxyURL(secret, proxy)
	if err != nil {
		return nil, err
	}
	app, err := ghclient.NewApp(ghclient.AppConfig{
		AppID: appID, PrivateKey: key, BaseURL: baseURL, ProxyURL: proxyURL,
	})
	if err != nil {
		return nil, err
	}
	// Drop stale resourceVersions of this secret so rotation doesn't grow the
	// map — but only stale versions: entries at the current version with a
	// different baseURL/proxy belong to other CRs sharing the Secret and must
	// survive.
	namePrefix := secret.Namespace + "/" + secret.Name + "/"
	for k := range a.apps {
		if strings.HasPrefix(k, namePrefix) && !strings.HasPrefix(k, versionPrefix) {
			delete(a.apps, k)
		}
	}
	a.apps[cacheKey] = app
	return app, nil
}

// Validate checks the secret is usable: a non-empty PAT or a parseable App
// credential, with a parseable composed proxy URL either way.
func (a *Apps) Validate(secret *corev1.Secret, baseURL, proxy string) error {
	if _, ok := secret.Data[KeyToken]; ok {
		if _, nonEmpty := Token(secret); !nonEmpty {
			return errors.New("secret key token is empty")
		}
		composed, err := ProxyURL(secret, proxy)
		if err != nil {
			return err
		}
		return ghclient.ValidateProxy(composed)
	}
	_, err := a.FromSecret(secret, baseURL, proxy)
	return err
}
