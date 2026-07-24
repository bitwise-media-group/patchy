// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package kubecfg connects the CLI to a cluster.
//
// It builds a cache-less controller-runtime client from the caller's own
// kubeconfig — one request per operation, no informers, no watches to warm up —
// because a CLI process lives for milliseconds and a cache would cost more than
// it saves.
//
// It also fetches server-rendered tables. Asking the API server for
// application/json;as=Table means the columns come from the CRD's own
// additionalPrinterColumns, so `patchy get findings` and `kubectl get findings`
// print the same columns forever, with no client-side list to keep in sync.
package kubecfg
