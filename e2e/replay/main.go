// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command replay signs a recorded webhook fixture with the shared secret and
// delivers it to a running controller — the local stand-in for GitHub.
//
//	replay -url http://localhost:8080/webhook -secret-file dev.secret \
//	    -event code_scanning_alert ../fixtures/webhooks/code_scanning_alert.created.json
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

func run() error {
	url := flag.String("url", "http://localhost:8080/webhook", "controller webhook endpoint")
	secretFile := flag.String("secret-file", "", "file holding the webhook secret (required)")
	event := flag.String("event", "", "X-GitHub-Event type (default: inferred from the fixture name)")
	flag.Parse()

	if flag.NArg() != 1 {
		return fmt.Errorf("usage: replay [flags] <fixture.json>")
	}
	fixture := flag.Arg(0)

	payload, err := os.ReadFile(fixture)
	if err != nil {
		return err
	}
	if *secretFile == "" {
		return fmt.Errorf("-secret-file is required")
	}
	rawSecret, err := os.ReadFile(*secretFile)
	if err != nil {
		return err
	}
	secret := []byte(strings.TrimRight(string(rawSecret), "\r\n"))

	eventType := *event
	if eventType == "" {
		eventType = eventFromName(fixture)
	}

	req, err := http.NewRequest(http.MethodPost, *url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Event", eventType)
	req.Header.Set("X-GitHub-Delivery", deliveryID())

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
