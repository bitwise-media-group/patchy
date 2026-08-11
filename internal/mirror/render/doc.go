// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package render produces an entry's rendered/manifests.yaml: the vendored
// chart rendered with its discovery values, exactly as `helm template
// <name> <vendor dir> --namespace ... --kube-version ... --include-crds -f
// ...` would print it. The output is committed — PR diffs show what would
// change in-cluster, and image discovery runs against it — so assembly
// must stay byte-stable: it drives the same helm install action
// (client-side dry run) the helm CLI drives and reassembles manifest plus
// hooks the way the template command does.
//
// The helm SDK pin is deliberate: rendering output can shift between helm
// versions, and the validate gate byte-compares regenerated output, so SDK
// upgrades are visible, reviewed events.
package render
