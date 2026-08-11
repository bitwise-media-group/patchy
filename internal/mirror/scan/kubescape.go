// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scan

import (
	"context"
	"os"
	"path/filepath"
)

// KubescapeResult is one configuration scan's outcome.
type KubescapeResult struct {
	// Skipped is set (with the reason) when the scan could not run —
	// the binary being absent is a warning, not a failure.
	Skipped string
	// Failed reports a non-zero scan under fail mode.
	Failed bool
	// Output is the scan's stdout, for narration.
	Output []byte
}

// Kubescape sweeps rendered manifests for configuration findings (RBAC
// breadth, security contexts). Those are usually inherent to what an
// upstream operator chart must do, so warn is the default mode; fail
// blocks on them.
func Kubescape(ctx context.Context, runner ToolRunner, renderedPath, mode string) (*KubescapeResult, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	if !runner.Look("kubescape") {
		return &KubescapeResult{Skipped: "kubescape not on PATH"}, nil
	}
	// Kubescape must not see a kubeconfig: given one, even a file scan
	// lists exception CRDs from the current cluster and merges them into
	// the results, so findings would depend on whatever cluster the
	// developer happens to be pointed at. An empty KUBECONFIG keeps file
	// scans self-contained and identical to CI.
	// --logger warning silences the progress-spinner UI, sidestepping an
	// upstream bug where it appends .txt to its /dev/null sink.
	empty := filepath.Join(os.TempDir(), "patchy-mirror-empty-kubeconfig")
	args := []string{"scan", renderedPath, "--logger", "warning"}
	if mode == "fail" {
		args = append(args, "--severity-threshold", "low")
	}
	stdout, err := runner.Run(ctx, "kubescape", args, []string{"KUBECONFIG=" + empty})
	result := &KubescapeResult{Output: stdout}
	if err != nil && mode == "fail" {
		result.Failed = true
	}
	// Warn mode never fails: configuration findings inform review.
	return result, nil
}
