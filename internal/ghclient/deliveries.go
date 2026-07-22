// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/go-github/v89/github"
)

// deliveryPageCap bounds one Deliveries scan (100 entries per page). A
// sweep that hits it reports itself incomplete rather than paging through
// an unbounded delivery log.
const deliveryPageCap = 50

// Delivery is one attempt from the App webhook's delivery log. Attempts of
// the same logical delivery (the original and its redeliveries) share a
// GUID.
type Delivery struct {
	ID          int64
	GUID        string
	DeliveredAt time.Time
	// Redelivery marks a redelivered attempt.
	Redelivery bool
	// OK reports whether the receiver answered 2xx.
	OK    bool
	Event string
}

// Deliveries walks the App webhook's delivery log newest-first, calling
// visit for each attempt until visit returns false or the log is
// exhausted. complete is false when the page cap ended the walk instead.
func (a *App) Deliveries(ctx context.Context, visit func(Delivery) bool) (complete bool, err error) {
	opts := &github.ListCursorOptions{PerPage: 100}
	for page := 0; page < deliveryPageCap; page++ {
		ds, resp, err := a.gh.Apps.ListHookDeliveries(ctx, opts)
		if err != nil {
			return false, fmt.Errorf("ghclient: list hook deliveries: %w", err)
		}
		for _, d := range ds {
			more := visit(Delivery{
				ID:          d.GetID(),
				GUID:        d.GetGUID(),
				DeliveredAt: d.GetDeliveredAt().Time,
				Redelivery:  d.GetRedelivery(),
				OK:          d.GetStatusCode() >= 200 && d.GetStatusCode() < 300,
				Event:       d.GetEvent(),
			})
			if !more {
				return true, nil
			}
		}
		if resp.Cursor == "" {
			return true, nil
		}
		opts.Cursor = resp.Cursor
	}
	return false, nil
}

// Redeliver asks GitHub to redeliver one App webhook delivery attempt.
func (a *App) Redeliver(ctx context.Context, deliveryID int64) error {
	_, _, err := a.gh.Apps.RedeliverHookDelivery(ctx, deliveryID)
	// The endpoint answers 202 Accepted — go-github's AcceptedError — on
	// success: the redelivery is queued, not performed inline.
	var accepted *github.AcceptedError
	if err != nil && !errors.As(err, &accepted) {
		return fmt.Errorf("ghclient: redeliver %d: %w", deliveryID, err)
	}
	return nil
}
