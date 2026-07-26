// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command replay delivers a recorded webhook fixture to a running
// integration-controller — the local stand-in for whichever provider sent it.
//
//	replay -url http://localhost:30079/github/webhooks -secret-file my.secret \
//	    -event code_scanning_alert ../fixtures/webhooks/code_scanning_alert.created.json
//
// Against the dev/dev-fake overlays, -dev-secret signs with their placeholder
// webhook secret so no secret file is needed:
//
//	replay -dev-secret ../fixtures/webhooks/code_scanning_alert.created.json
//
// A Security Command Center fixture is a Pub/Sub push envelope, which
// authenticates with a bearer token rather than an HMAC — Pub/Sub composes the
// message, so there is nothing for the sender to sign. Supply one Google
// issued for the audience the Integration expects:
//
//	replay -bearer "$(gcloud auth print-identity-token \
//	    --audiences=https://patchy.example/google-cloud/webhooks)" \
//	    ../fixtures/webhooks/scc.notification.json
//
// The route is inferred from the fixture name, so -url is only needed when
// the controller is not on the default port.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(1)
	}
}

// devWebhookSecret is the placeholder the dev overlay ships in its
// patchy-github Secret (deploy/kustomize/overlays/dev/secret-dev.yaml);
// -dev-secret signs with it so replaying against the dev/dev-fake stacks
// needs no secret file.
const devWebhookSecret = "dev-webhook-secret-replace-me"

// defaultHost is where the dev overlay exposes the receiver.
const defaultHost = "http://localhost:30079"

func run() error {
	url := flag.String("url", "", "controller webhook endpoint (default: inferred from the fixture)")
	secretFile := flag.String("secret-file", "", "file holding the webhook secret")
	devSecret := flag.Bool("dev-secret", false, "sign with the dev overlay's placeholder webhook secret")
	bearer := flag.String("bearer", "",
		"OIDC token for a Pub/Sub push fixture, e.g. "+
			"$(gcloud auth print-identity-token --audiences=<the Integration's audience>)")
	event := flag.String("event", "", "event type (default: inferred from the fixture name)")
	flag.Parse()

	if flag.NArg() != 1 {
		return fmt.Errorf("usage: replay [flags] <fixture.json>")
	}
	fixture := flag.Arg(0)

	payload, err := os.ReadFile(fixture)
	if err != nil {
		return err
	}

	eventType := *event
	if eventType == "" {
		eventType = eventFromName(fixture)
	}
	// The provider decides both the route and how the delivery is
	// authenticated, and the fixture name is what names the provider.
	pubsub := isPubSub(fixture)
	endpoint := *url
	if endpoint == "" {
		endpoint = defaultHost + defaultPath(pubsub)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	if pubsub {
		if *bearer == "" {
			return fmt.Errorf(
				"a Pub/Sub push fixture needs -bearer: the message is composed by Pub/Sub, so there is " +
					"nothing for the sender to sign and the OIDC token is the only credential")
		}
		req.Header.Set("Authorization", "Bearer "+*bearer)
	} else {
		if *bearer != "" {
			return fmt.Errorf("-bearer applies to Pub/Sub push fixtures; GitHub deliveries are signed")
		}
		secret, err := githubSecret(*devSecret, *secretFile)
		if err != nil {
			return err
		}
		mac := hmac.New(sha256.New, secret)
		mac.Write(payload)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		req.Header.Set("X-GitHub-Event", eventType)
		req.Header.Set("X-GitHub-Delivery", deliveryID())
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	fmt.Printf("%s -> %s %s\n", filepath.Base(fixture), eventType, resp.Status)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("delivery rejected: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

// githubSecret resolves the HMAC secret for a GitHub delivery.
func githubSecret(dev bool, file string) ([]byte, error) {
	switch {
	case dev && file != "":
		return nil, fmt.Errorf("-dev-secret and -secret-file are mutually exclusive")
	case dev:
		return []byte(devWebhookSecret), nil
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		return []byte(strings.TrimRight(string(raw), "\r\n")), nil
	default:
		return nil, fmt.Errorf("one of -secret-file or -dev-secret is required")
	}
}

// isPubSub reports whether the fixture is a Pub/Sub push envelope rather than
// a GitHub delivery, by the provider prefix in its name.
func isPubSub(path string) bool {
	return strings.HasPrefix(filepath.Base(path), "scc.")
}

// defaultPath is the receiver route for the fixture's provider.
func defaultPath(pubsub bool) string {
	if pubsub {
		return "/google-cloud/webhooks"
	}
	return "/github/webhooks"
}

// eventFromName infers the event type from the fixture's name
// ("code_scanning_alert.created.json" → "code_scanning_alert").
func eventFromName(path string) string {
	name := filepath.Base(path)
	if i := strings.Index(name, "."); i > 0 {
		return name[:i]
	}
	return name
}

func deliveryID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "replay-fixed-delivery-id"
	}
	return hex.EncodeToString(b[:])
}
