// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"fmt"
	"time"

	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// defaultDebounce coalesces informer bursts (an ingest window closing, a
// scan delivering hundreds of alerts) into one refetch signal.
const defaultDebounce = 500 * time.Millisecond

// StartWatch registers change handlers on the Finding/FindingRollup and
// Integration/Forge informers and publishes debounced findings-changed /
// config-changed notifications to the SSE broker. It blocks until ctx is
// cancelled, so it slots straight into a manager runnable — running after
// the cache has synced and stopping with the manager.
func (s *Server) StartWatch(ctx context.Context, c cache.Cache) error {
	watch := func(signal chan<- struct{}, objs ...client.Object) error {
		notify := func() {
			select {
			case signal <- struct{}{}:
			default:
			}
		}
		for _, obj := range objs {
			informer, err := c.GetInformer(ctx, obj)
			if err != nil {
				return fmt.Errorf("watch %T: %w", obj, err)
			}
			_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
				AddFunc: func(any) { notify() },
				UpdateFunc: func(oldObj, newObj any) {
					if resourceChanged(oldObj, newObj) {
						notify()
					}
				},
				DeleteFunc: func(any) { notify() },
			})
			if err != nil {
				return fmt.Errorf("watch %T: %w", obj, err)
			}
		}
		return nil
	}

	findings := make(chan struct{}, 1)
	if err := watch(findings, &v1alpha1.Finding{}, &v1alpha1.FindingRollup{}); err != nil {
		return err
	}
	config := make(chan struct{}, 1)
	if err := watch(config, &v1alpha1.Integration{}, &v1alpha1.Forge{}); err != nil {
		return err
	}
	go s.debounceLoop(ctx, config, eventConfigChanged)
	s.debounceLoop(ctx, findings, eventFindingsChanged)
	return nil
}

// debounceLoop publishes at most one notification of event per debounce
// window: the first signal arms the timer, further signals are absorbed
// into the pending publish.
func (s *Server) debounceLoop(ctx context.Context, signal <-chan struct{}, event string) {
	debounce := s.debounce
	if debounce <= 0 {
		debounce = defaultDebounce
	}
	timer := time.NewTimer(debounce)
	timer.Stop()
	pending := false
	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-signal:
			if !pending {
				pending = true
				timer.Reset(debounce)
			}
		case <-timer.C:
			if pending {
				pending = false
				s.broker.publish(event)
			}
		}
	}
}

// resourceChanged filters informer resyncs: an update whose resourceVersion
// did not move carries no new state.
func resourceChanged(oldObj, newObj any) bool {
	o, ok1 := oldObj.(client.Object)
	n, ok2 := newObj.(client.Object)
	if !ok1 || !ok2 {
		return true
	}
	return o.GetResourceVersion() != n.GetResourceVersion()
}
