// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/bitwise-media-group/patchy/internal/mirror/helmchart"
	"github.com/bitwise-media-group/patchy/internal/mirror/imageref"
	"github.com/bitwise-media-group/patchy/internal/mirror/ocireg"
	"github.com/bitwise-media-group/patchy/internal/mirror/scan"
	"github.com/bitwise-media-group/patchy/internal/mirror/sign"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/verify"
)

// defaultWorkers bounds concurrent registry operations per stage.
const defaultWorkers = 4

// Config wires an Engine.
type Config struct {
	// Root is the mirror store root (the directory holding mirror.yaml).
	Root string
	// Global is the loaded mirror.yaml.
	Global *spec.Config
	// Now is the wall clock; injectable for tests. Only upgrade-path
	// stages may consult it.
	Now func() time.Time
	// Workers bounds concurrent registry operations (default 4).
	Workers int
	// OnEvent receives narration events (nil discards them).
	OnEvent func(Event)
	// Registry performs OCI operations (nil: ambient keychain).
	Registry *ocireg.Client
	// HTTP performs helm-repository fetches (nil: http.DefaultClient).
	HTTP *http.Client
	// Verify checks one signature subject (nil: the real cosign-backed
	// verifier). Tests inject key-based verification.
	Verify func(context.Context, verify.Subject) error
	// NewSigner builds a signer for a resolved signing config (nil: the
	// real keyless/KMS signer). Tests inject a key-file signer.
	NewSigner func(*spec.Signing) (ArtifactSigner, error)
	// PushChart pushes a chart tgz to ref, returning the manifest digest
	// (nil: the helm-layout push).
	PushChart func(tgz []byte, ref string) (string, error)
	// Tools runs external tool binaries — scanners and cosign (nil: the
	// real PATH).
	Tools scan.ToolRunner
	// ImageScanners overrides the enabled scanner roster (nil: built
	// from the global scan config). Tests inject fakes.
	ImageScanners []scan.ImageScanner
}

// ArtifactSigner signs one OCI artifact reference.
type ArtifactSigner interface {
	Sign(ctx context.Context, ref string, recursive bool) error
}

// Engine runs the mirror pipeline stages over one store.
type Engine struct {
	root          string
	global        *spec.Config
	now           func() time.Time
	workers       int
	onEvent       func(Event)
	reg           *ocireg.Client
	puller        *helmchart.Puller
	verifyFn      func(context.Context, verify.Subject) error
	newSigner     func(*spec.Signing) (ArtifactSigner, error)
	pushChart     func(tgz []byte, ref string) (string, error)
	tools         scan.ToolRunner
	imageScanners []scan.ImageScanner
}

// New builds an Engine.
func New(cfg Config) *Engine {
	e := &Engine{
		root:          cfg.Root,
		global:        cfg.Global,
		now:           cfg.Now,
		workers:       cfg.Workers,
		onEvent:       cfg.OnEvent,
		reg:           cfg.Registry,
		verifyFn:      cfg.Verify,
		newSigner:     cfg.NewSigner,
		pushChart:     cfg.PushChart,
		tools:         cfg.Tools,
		imageScanners: cfg.ImageScanners,
	}
	if e.tools == nil {
		e.tools = scan.ExecRunner{}
	}
	if e.verifyFn == nil {
		e.verifyFn = func(ctx context.Context, s verify.Subject) error {
			return verify.Verify(ctx, e.tools, s)
		}
	}
	if e.newSigner == nil {
		e.newSigner = func(s *spec.Signing) (ArtifactSigner, error) { return sign.New(s, e.tools) }
	}
	if e.pushChart == nil {
		e.pushChart = helmchart.Push
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.workers <= 0 {
		e.workers = defaultWorkers
	}
	if e.onEvent == nil {
		e.onEvent = func(Event) {}
	}
	if e.reg == nil {
		e.reg = ocireg.New(nil)
	}
	e.puller = &helmchart.Puller{
		Registry: e.reg,
		HTTP:     cfg.HTTP,
		Rewrites: e.global.SourceRegistryRewrites,
	}
	return e
}

// Entries discovers every entry in the store.
func (e *Engine) Entries() ([]spec.Entry, error) {
	return spec.Discover(e.root)
}

// Entry loads one entry by name.
func (e *Engine) Entry(name string) (spec.Entry, error) {
	return spec.LoadEntry(e.root, name)
}

// rewrite reroutes one pull ref through the configured source rewrites.
func (e *Engine) rewrite(ref string) string {
	return imageref.Rewrite(ref, e.global.SourceRegistryRewrites)
}

// imageTarget computes the mirror target for a canonical source reference:
// <registry>/<imageNamespace>/<source repo path>:<tag>.
func (e *Engine) imageTarget(source string) (string, error) {
	ref, err := imageref.Parse(source)
	if err != nil {
		return "", err
	}
	tag := ref.Tag
	if tag == "" {
		tag = "latest"
	}
	return fmt.Sprintf("%s/%s/%s:%s", e.global.Registry.URL, e.global.Registry.ImageNamespace, ref.Repository, tag), nil
}

// chartRepo resolves a chart entry's publish repository path under the
// registry URL.
func (e *Engine) chartRepo(entry spec.Entry) string {
	if entry.Chart.Publish.ChartRepo != "" {
		return entry.Chart.Publish.ChartRepo
	}
	return e.global.Registry.ChartNamespace + "/" + entry.Name
}

// artifactRepo resolves an artifact entry's publish repository path under
// the registry URL.
func (e *Engine) artifactRepo(entry spec.Entry) string {
	if entry.Artifact.Publish.Repo != "" {
		return entry.Artifact.Publish.Repo
	}
	return e.global.Registry.ArtifactNamespace + "/" + entry.Artifact.Artifact.Ref
}

// vendorDir is a chart entry's vendor tree root.
func vendorDir(entry spec.Entry) string { return filepath.Join(entry.Dir, "vendor") }

// renderedPath is a chart entry's committed rendered output.
func renderedPath(entry spec.Entry) string {
	return filepath.Join(entry.Dir, "rendered", "manifests.yaml")
}
