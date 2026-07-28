// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/wiz"
)

// WizPath is the receiver route Wiz automation actions target. It matches the
// path integration_controller advertises on status.webhookPath, which is
// derived from the provider name. Both Wiz feeds (Issues and Defend) share
// it: an automation action carries no event-type header, so the decoder
// discriminates by payload shape instead.
const WizPath = "/" + string(v1alpha1.IntegrationProviderWiz) + "/webhooks"

// wizDecoder labels a Wiz delivery. The event type comes from the body shape
// (issue vs threat vs Wiz's test delivery, which maps to ping); the delivery
// id is a digest of the body, because a Wiz automation action sends no
// delivery GUID.
func wizDecoder(_ *http.Request, body []byte) (eventType, deliveryID string, err error) {
	event, err := wiz.Detect(body)
	if err != nil {
		return "", "", err
	}
	return event, wiz.DeliveryID(body), nil
}
