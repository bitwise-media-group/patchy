// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentrun

import (
	"bytes"
	"context"
	"encoding/base64"
	"os/exec"
)

// runScript executes the agent-written commit script once, from the
// repository root, capturing combined output for diagnostics. It runs inside
// the disposable pod — agent-generated shell never executes on a controller.
func runScript(ctx context.Context, dir, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", script)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func encodeB64(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }
