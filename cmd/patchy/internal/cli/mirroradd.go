// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/internal/mirror/helmchart"
	"github.com/bitwise-media-group/patchy/internal/mirror/imageref"
	"github.com/bitwise-media-group/patchy/internal/mirror/ocireg"
	"github.com/bitwise-media-group/patchy/internal/mirror/semverpick"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// newMirrorAddCmd scaffolds a new entry: detect what the URL points at,
// pin the newest stable version, and write the entry directory. The first
// upgrade vendors it; add itself never pulls the archive and never commits.
func newMirrorAddCmd(opts *Options, f *mirrorFlags) *cobra.Command {
	var (
		url        string
		entryType  string
		version    string
		constraint string
		lockstep   string
	)
	cmd := &cobra.Command{
		Use:   "add [name]",
		Short: "Scaffold a new chart or artifact entry",
		Long: "Create the entry directory for a new mirrored chart or OCI artifact: detect\n" +
			"what --url points at (an https:// helm repository, an oci:// chart\n" +
			"repository, or a bare artifact reference), pin the newest stable version,\n" +
			"default the constraint to stay-in-major, and scaffold manifest.yaml.\n\n" +
			"The name defaults to the URL's last path segment. On first use in an empty\n" +
			"directory, add also writes a starter mirror.yaml with commented defaults.\n" +
			"Nothing is vendored and nothing is committed — review the scaffold, then run\n" +
			"`patchy mirror upgrade <name>` to vendor and lock it.",
		Example: "  patchy mirror add --url oci://ghcr.io/open-telemetry/" +
			"opentelemetry-helm-charts/opentelemetry-collector\n" +
			"  patchy mirror add dex --url https://charts.dexidp.io\n" +
			"  patchy mirror add runner-bundle --url ghcr.io/example/bundle --type artifact",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				return errUsage(errors.New("--url is required"))
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return runMirrorAdd(cmd.Context(), cmd, opts, f, addSpec{
				name: name, url: url, entryType: entryType,
				version: version, constraint: constraint, lockstep: lockstep,
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&url, "url", "", "upstream location: https:// helm repo, oci:// chart repo, or artifact ref")
	fl.StringVar(&entryType, "type", "", "force the entry type: chart or artifact (default: detect)")
	fl.StringVar(&version, "version", "", "pin this version instead of the newest stable")
	fl.StringVar(&constraint, "constraint", "", "version constraint (default: stay within the pinned major)")
	fl.StringVar(&lockstep, "lockstep", "", "lockstep group this entry bumps with")
	_ = cmd.RegisterFlagCompletionFunc("type", fixedCompletion([]string{"chart", "artifact"}))
	return cmd
}

// addSpec carries one add invocation.
type addSpec struct {
	name, url, entryType, version, constraint, lockstep string
}

// runMirrorAdd detects, pins and scaffolds.
func runMirrorAdd(ctx context.Context, cmd *cobra.Command, opts *Options, f *mirrorFlags, a addSpec) error {
	root, created, err := mirrorRootOrInit(cmd, opts, f)
	if err != nil {
		return err
	}
	if created {
		notef(opts.ErrOut, "patchy: created %s with commented defaults — review it before publishing\n",
			filepath.Join(root, spec.ConfigFile))
	}

	kind, chartRepo, chartName, artifactRef, err := detectAddTarget(ctx, a)
	if err != nil {
		return err
	}
	if kind == spec.KindArtifact {
		// The manifest records the repository; any tag in the URL only
		// influences nothing — versions pin explicitly.
		ref, err := imageref.Parse(artifactRef)
		if err != nil {
			return err
		}
		artifactRef = ref.Repository
	}
	name := a.name
	if name == "" {
		if kind == spec.KindChart {
			name = chartName
		} else {
			name = artifactRef[strings.LastIndex(artifactRef, "/")+1:]
		}
	}

	version := a.version
	if version == "" {
		version, err = newestStable(ctx, kind, chartRepo, chartName, artifactRef)
		if err != nil {
			return err
		}
		notef(opts.ErrOut, "patchy: pinning newest stable version %s\n", version)
	}
	constraint := a.constraint
	if constraint == "" {
		constraint, err = stayInMajor(version)
		if err != nil {
			return err
		}
	}

	dir := filepath.Join(root, "charts", name)
	if kind == spec.KindArtifact {
		dir = filepath.Join(root, "artifacts", name)
	}
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("entry %s already exists at %s", name, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var manifest []byte
	if kind == spec.KindChart {
		manifest = scaffoldChartManifest(name, chartRepo, chartName, version, constraint, a.lockstep)
		if err := os.MkdirAll(filepath.Join(dir, "values"), 0o755); err != nil {
			return err
		}
		values := "# Values used to render the chart for review and image discovery.\n" +
			"# Keep this the closest thing to how the platform actually deploys it.\n{}\n"
		if err := os.WriteFile(filepath.Join(dir, "values", "discovery.yaml"), []byte(values), 0o644); err != nil {
			return err
		}
	} else {
		manifest = scaffoldArtifactManifest(name, artifactRef, version, constraint, a.lockstep)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), manifest, 0o644); err != nil {
		return err
	}
	notef(opts.ErrOut, "patchy: scaffolded %s (%s %s)\n", dir, strings.ToLower(kind), version)
	notef(opts.ErrOut, "patchy: review the manifest (verifyUpstream, discovery values), "+
		"then run: patchy mirror upgrade %s\n", name)
	_, _ = fmt.Fprintln(opts.Out, name)
	return nil
}

// mirrorRootOrInit resolves the store root, scaffolding mirror.yaml in the
// working directory when no store exists yet. Only the no-store miss earns
// a scaffold — any other failure (a broken CLI config, say) propagates.
func mirrorRootOrInit(cmd *cobra.Command, opts *Options, f *mirrorFlags) (string, bool, error) {
	root, err := mirrorRoot(cmd, opts, f)
	if err == nil {
		return root, false, nil
	}
	if !errors.Is(err, spec.ErrNoRoot) {
		return "", false, err
	}
	dir := f.directory
	if dir == "" {
		dir = "."
	}
	abs, absErr := filepath.Abs(dir)
	if absErr != nil {
		return "", false, absErr
	}
	if writeErr := os.WriteFile(filepath.Join(abs, spec.ConfigFile), scaffoldMirrorConfig(), 0o644); writeErr != nil {
		return "", false, fmt.Errorf("no mirror.yaml found and could not create one: %w", writeErr)
	}
	return abs, true, nil
}

// detectAddTarget classifies the URL.
func detectAddTarget(ctx context.Context, a addSpec) (kind, chartRepo, chartName, artifactRef string, err error) {
	switch {
	case strings.HasPrefix(a.url, "https://"), strings.HasPrefix(a.url, "http://"):
		// A helm repository; the entry name doubles as the chart name.
		if a.entryType == "artifact" {
			return "", "", "", "", errUsage(errors.New(
				"an https:// URL is a helm repository; artifacts need a registry reference"))
		}
		if a.name == "" {
			return "", "", "", "", errUsage(errors.New("an https:// repo does not name the chart; pass the entry name argument"))
		}
		return spec.KindChart, a.url, a.name, "", nil
	case strings.HasPrefix(a.url, "oci://"):
		path := strings.TrimPrefix(a.url, "oci://")
		repo, last := splitLast(path)
		switch a.entryType {
		case "chart":
			return spec.KindChart, "oci://" + repo, last, "", nil
		case "artifact":
			return spec.KindArtifact, "", "", path, nil
		case "":
			isChart, err := ociPathIsChart(ctx, path)
			if err != nil {
				return "", "", "", "", err
			}
			if isChart {
				return spec.KindChart, "oci://" + repo, last, "", nil
			}
			return spec.KindArtifact, "", "", path, nil
		default:
			return "", "", "", "", errUsage(fmt.Errorf("unknown --type %q (want chart or artifact)", a.entryType))
		}
	default:
		// A bare reference is an artifact.
		if a.entryType == "chart" {
			return "", "", "", "", errUsage(errors.New("a chart needs an oci:// or https:// URL"))
		}
		return spec.KindArtifact, "", "", a.url, nil
	}
}

// ociPathIsChart probes the newest release tag's config media type.
func ociPathIsChart(ctx context.Context, path string) (bool, error) {
	reg := ocireg.New(nil)
	tags, err := reg.Tags(ctx, path)
	if err != nil {
		return false, fmt.Errorf("cannot detect what %s is (pass --type): %w", path, err)
	}
	releases := semverpick.Releases(tags)
	if len(releases) == 0 {
		return false, fmt.Errorf("%s has no release tags; pass --type and --version", path)
	}
	mt, err := reg.ConfigMediaType(ctx, path+":"+releases[0])
	if err != nil {
		return false, fmt.Errorf("cannot detect what %s is (pass --type): %w", path, err)
	}
	return mt == helmchart.ChartConfigMediaType, nil
}

// newestStable resolves the newest release version of the target.
func newestStable(ctx context.Context, kind, chartRepo, chartName, artifactRef string) (string, error) {
	var candidates []string
	var err error
	switch {
	case kind == spec.KindChart && strings.HasPrefix(chartRepo, "oci://"):
		candidates, err = ocireg.New(nil).Tags(ctx, strings.TrimPrefix(chartRepo, "oci://")+"/"+chartName)
	case kind == spec.KindChart:
		p := &helmchart.Puller{Registry: ocireg.New(nil)}
		candidates, err = p.Versions(ctx, chartRepo, chartName)
	default:
		candidates, err = ocireg.New(nil).Tags(ctx, artifactRef)
	}
	if err != nil {
		return "", err
	}
	releases := semverpick.Releases(candidates)
	if len(releases) == 0 {
		return "", errors.New("no stable versions found; pass --version")
	}
	return releases[0], nil
}

// stayInMajor derives the default constraint from the pinned version.
func stayInMajor(version string) (string, error) {
	v, err := semver.NewVersion(version)
	if err != nil {
		return "", fmt.Errorf("parse version %q: %w", version, err)
	}
	return fmt.Sprintf(">=%s <%d.0.0", strings.TrimPrefix(version, "v"), v.Major()+1), nil
}

// splitLast splits a path at its last segment.
func splitLast(path string) (parent, last string) {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return "", path
	}
	return path[:i], path[i+1:]
}
