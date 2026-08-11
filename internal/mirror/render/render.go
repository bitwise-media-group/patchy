// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package render

import (
	"context"
	"fmt"
	"strings"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/cli/values"
	"helm.sh/helm/v4/pkg/getter"
	release "helm.sh/helm/v4/pkg/release/v1"
)

// Input configures one render.
type Input struct {
	// ChartDir is the vendored chart directory (vendor/<chart name>).
	ChartDir string
	// ReleaseName is the helm release name; the mirror uses the chart
	// name, matching `helm template <name> <dir>`.
	ReleaseName string
	// Namespace to render into.
	Namespace string
	// KubeVersion for .Capabilities.KubeVersion, e.g. "1.34.0".
	KubeVersion string
	// APIVersions are extra .Capabilities.APIVersions entries.
	APIVersions []string
	// ValuesFiles are merged in order, later files winning.
	ValuesFiles []string
}

// Render renders the chart client-side and returns the bytes helm template
// would print: the release manifest, then each hook prefixed with its
// source header.
func Render(ctx context.Context, in Input) ([]byte, error) {
	chrt, err := loader.Load(in.ChartDir)
	if err != nil {
		return nil, fmt.Errorf("load chart %s: %w", in.ChartDir, err)
	}

	kubeVersion, err := common.ParseKubeVersion(in.KubeVersion)
	if err != nil {
		return nil, fmt.Errorf("invalid kube version %q: %w", in.KubeVersion, err)
	}

	valueOpts := &values.Options{ValueFiles: in.ValuesFiles}
	vals, err := valueOpts.MergeValues(getter.All(cli.New()))
	if err != nil {
		return nil, fmt.Errorf("merge values: %w", err)
	}

	client := action.NewInstall(new(action.Configuration))
	client.DryRunStrategy = action.DryRunClient
	client.ReleaseName = in.ReleaseName
	client.Replace = true // skip the name availability check, like helm template
	client.IncludeCRDs = true
	client.Namespace = in.Namespace
	client.KubeVersion = kubeVersion
	client.APIVersions = common.VersionSet(in.APIVersions)

	ri, err := client.RunWithContext(ctx, chrt, vals)
	if err != nil {
		return nil, fmt.Errorf("render chart: %w", err)
	}
	rel, err := toV1Release(ri)
	if err != nil {
		return nil, err
	}

	// Reassemble exactly as the helm template command does: trimmed
	// manifest, newline, then every hook under a source header.
	var b strings.Builder
	fmt.Fprintln(&b, strings.TrimSpace(rel.Manifest))
	for _, h := range rel.Hooks {
		fmt.Fprintf(&b, "---\n# Source: %s\n%s\n", h.Path, h.Manifest)
	}
	return []byte(b.String()), nil
}

// toV1Release unwraps the action result to the v1 release shape.
func toV1Release(ri any) (*release.Release, error) {
	switch r := ri.(type) {
	case release.Release:
		return &r, nil
	case *release.Release:
		return r, nil
	default:
		return nil, fmt.Errorf("unsupported release type %T", ri)
	}
}
