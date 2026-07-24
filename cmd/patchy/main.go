// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command patchy is the workstation client for the patchy pipeline: it lists,
// inspects, reviews and acts on the custom resources that carry the state
// machine.
//
// It talks to the Kubernetes API directly with the caller's own kubeconfig —
// never through a controller or the status server — so what it may do is
// exactly what that identity's RBAC allows.
package main

import (
	"os"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
