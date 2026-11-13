// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bitwise-media-group/patchy/internal/build/buildx"
	"github.com/bitwise-media-group/patchy/internal/build/expand"
	"github.com/bitwise-media-group/patchy/internal/build/fluxartifact"
	"github.com/bitwise-media-group/patchy/internal/mirror/allowlist"
	"github.com/bitwise-media-group/patchy/internal/mirror/ocireg"
	"github.com/bitwise-media-group/patchy/internal/mirror/scan"
	"github.com/bitwise-media-group/patchy/internal/mirror/sign"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/verify"
)

// Builder builds one image into an OCI layout directory; buildx.Builder in
// production, a fake writing a layout in tests.
type Builder interface {
	Build(ctx context.Context, r buildx.Request) error
}

// Config wires an Engine. The seams mirror mirror.Config's, so the two
// engines are tested the same way.
type Config struct {
	// Workdir is the directory relative entry paths and the allowlists
	// (security/allowlists/<name>.yaml) resolve against (default: the
	// process working directory).
	Workdir string
	// Global is the resolved registries, signing and scan blocks.
	Global *spec.Config
	// Spec is the validated build block.
	Spec *Spec
	// Registry performs OCI operations (nil: ambient keychain).
	Registry *ocireg.Client
	// Tools runs external binaries — docker, scanners, cosign (nil: the
	// real PATH).
	Tools scan.ToolRunner
	// ArchiveScanners overrides the image scanner roster (nil: built from
	// the scan config).
	ArchiveScanners []scan.ArchiveScanner
	// Verify checks one signature subject (nil: cosign).
	Verify func(context.Context, verify.Subject) error
	// NewSigner builds a signer for a resolved signing config (nil: the
	// real keyless/KMS signer).
	NewSigner func(*spec.Signing) (sign.ArtifactSigner, error)
	// Builder builds images (nil: docker buildx through Tools).
	Builder Builder
	// Now is the wall clock: the allowlist expiry horizon and the default
	// PATCHY_TIMESTAMP.
	Now func() time.Time
	// LookupEnv resolves ${VAR} references and SOURCE_DATE_EPOCH (nil:
	// os.LookupEnv).
	LookupEnv func(string) (string, bool)
	// OnEvent receives narration (nil discards it).
	OnEvent func(Event)
}

// Engine builds, scans, publishes and signs the configured entries.
type Engine struct {
	workdir         string
	global          *spec.Config
	spec            *Spec
	reg             *ocireg.Client
	tools           scan.ToolRunner
	archiveScanners []scan.ArchiveScanner
	ensurer         sign.Ensurer
	builder         Builder
	now             func() time.Time
	lookupEnv       func(string) (string, bool)
	lookup          expand.Lookup
	onEvent         func(Event)
}

// New builds an Engine, fixing the run's PATCHY_TIMESTAMP once.
func New(cfg Config) (*Engine, error) {
	if cfg.Global == nil || cfg.Spec == nil {
		return nil, errors.New("build engine needs the resolved config and build spec")
	}
	e := &Engine{
		workdir:         cfg.Workdir,
		global:          cfg.Global,
		spec:            cfg.Spec,
		reg:             cfg.Registry,
		tools:           cfg.Tools,
		archiveScanners: cfg.ArchiveScanners,
		builder:         cfg.Builder,
		now:             cfg.Now,
		lookupEnv:       cfg.LookupEnv,
		onEvent:         cfg.OnEvent,
	}
	if e.workdir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		e.workdir = wd
	}
	if e.tools == nil {
		e.tools = scan.ExecRunner{}
	}
	if e.reg == nil {
		e.reg = ocireg.New(nil)
	}
	if e.builder == nil {
		e.builder = buildx.Builder{Runner: e.tools}
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.lookupEnv == nil {
		e.lookupEnv = os.LookupEnv
	}
	if e.onEvent == nil {
		e.onEvent = func(Event) {}
	}
	e.ensurer = sign.Ensurer{Verify: cfg.Verify, NewSigner: cfg.NewSigner}
	if e.ensurer.Verify == nil {
		e.ensurer.Verify = func(ctx context.Context, s verify.Subject) error { return verify.Verify(ctx, e.tools, s) }
	}
	if e.ensurer.NewSigner == nil {
		e.ensurer.NewSigner = func(s *spec.Signing) (sign.ArtifactSigner, error) { return sign.New(s, e.tools) }
	}
	ts, err := expand.Timestamp(e.lookupEnv, e.now())
	if err != nil {
		return nil, err
	}
	e.lookup = expand.WithBuiltins(e.lookupEnv, ts)
	return e, nil
}

// Options tunes one Build call.
type Options struct {
	// DryRun builds and scans, then reports would-push rows without
	// contacting any registry.
	DryRun bool
	// Registries restricts publishing to the named registries (nil: all).
	Registries []string
	// Tags replaces the configured tag templates for this run (still
	// expanded).
	Tags []string
}

// Entries lists every configured entry.
func (e *Engine) Entries() []Entry { return e.spec.Entries() }

// Entry looks one entry up by name.
func (e *Engine) Entry(name string) (Entry, error) {
	entry, ok := e.spec.Entry(name)
	if !ok {
		entries := e.spec.Entries()
		names := make([]string, 0, len(entries))
		for _, en := range entries {
			names = append(names, en.Name)
		}
		return Entry{}, fmt.Errorf("no build entry %q (configured: %s)", name, strings.Join(names, ", "))
	}
	return entry, nil
}

// SelectRegistries resolves registry names (nil: all); the CLI validates
// --registry through it.
func (e *Engine) SelectRegistries(names []string) ([]spec.Registry, error) {
	return e.global.SelectRegistries(names)
}

// Build builds, scans and — when the scan passes — publishes and signs one
// entry. The result is returned even on failure (with whatever was
// learned: the scan report, the records pushed before an error).
func (e *Engine) Build(ctx context.Context, name string, opts Options) (*Result, error) {
	entry, err := e.Entry(name)
	if err != nil {
		return nil, err
	}
	res := &Result{Name: entry.Name, Kind: entry.Kind}
	regs, err := e.global.SelectRegistries(opts.Registries)
	if err != nil {
		return res, err
	}
	tags, err := e.tags(entry, opts.Tags)
	if err != nil {
		return res, err
	}
	res.Tags = tags
	if entry.Kind == KindManifests {
		return res, e.buildManifests(ctx, entry, tags, regs, opts.DryRun, res)
	}
	return res, e.buildImage(ctx, entry, tags, regs, opts.DryRun, res)
}

// tagRe is the OCI distribution tag grammar.
var tagRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// tags expands and validates an entry's tags (or the run's override),
// before anything is built.
func (e *Engine) tags(entry Entry, override []string) ([]string, error) {
	templates := entry.Tags
	if len(override) > 0 {
		templates = override
	}
	if len(templates) == 0 {
		return nil, errors.New("no tags: set build.tags or the entry's tags, or pass --tag")
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range templates {
		v, err := expand.Expand(t, e.lookup)
		if err != nil {
			return nil, fmt.Errorf("tag %q: %w", t, err)
		}
		if !tagRe.MatchString(v) {
			return nil, fmt.Errorf("tag %q expands to %q, which is not a valid OCI tag", t, v)
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}

// expandField expands one templated field, naming it on failure.
func (e *Engine) expandField(field, tmpl string) (string, error) {
	v, err := expand.Expand(tmpl, e.lookup)
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", field, tmpl, err)
	}
	return v, nil
}

// path resolves a config path against the working directory.
func (e *Engine) path(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.workdir, p)
}

// AllowlistPath is an image entry's allowlist file.
func (e *Engine) AllowlistPath(name string) string {
	return filepath.Join(e.workdir, AllowlistDir, name+".yaml")
}

// relPath shows a path relative to the working directory when it lies
// beneath it.
func (e *Engine) relPath(p string) string {
	if rel, err := filepath.Rel(e.workdir, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}

// workspace creates a scratch directory for one entry's build outputs.
func workspace() (string, func(), error) {
	dir, err := os.MkdirTemp("", "patchy-build-*")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// buildImage runs the image flow: resolve args and secrets, lint the allowlist, build,
// scan every platform, and publish only when nothing blocks.
func (e *Engine) buildImage(ctx context.Context, entry Entry, tags []string, regs []spec.Registry,
	dryRun bool, res *Result) error {
	in, err := e.buildInputs(entry)
	if err != nil {
		return err
	}
	if err := e.enforcePolicy(entry, in.args); err != nil {
		return err
	}
	policy := spec.EffectiveScanPolicy(e.global.Scan, entry.Scan)
	report := &ScanReport{}
	res.Scan = report
	var allow *spec.Allowlist
	if policy.Enabled {
		if allow, err = e.activeAllowlist(entry, policy, report); err != nil {
			return err
		}
	}
	work, cleanup, err := workspace()
	if err != nil {
		return err
	}
	defer cleanup()
	b, err := e.buildLayout(ctx, entry, in, work)
	if err != nil {
		return err
	}
	res.Digest = b.digest.String()
	if err := e.scanImage(ctx, entry, b, policy, allow, work, report); err != nil {
		return err
	}
	if report.Failed() {
		return fmt.Errorf("%d blocking finding(s); nothing pushed", len(report.Blocking))
	}
	res.Records, err = e.publish(ctx, entry, b, tags, regs, true, dryRun)
	return err
}

// buildInputs are an image build's resolved inputs.
type buildInputs struct {
	args    map[string]string
	secrets []buildx.Secret
}

// buildInputs resolves an image's build args and secrets before anything
// is built, so a bad template or a missing secret fails fast.
func (e *Engine) buildInputs(entry Entry) (buildInputs, error) {
	args, err := e.args(entry.Image)
	if err != nil {
		return buildInputs{}, err
	}
	secrets, err := e.secrets(entry)
	if err != nil {
		return buildInputs{}, err
	}
	return buildInputs{args: args, secrets: secrets}, nil
}

// args expands an image's build-arg values; keys keep their case.
func (e *Engine) args(img *Image) (map[string]string, error) {
	out := make(map[string]string, len(img.Args))
	for k, v := range img.Args {
		x, err := e.expandField("build arg "+k, v)
		if err != nil {
			return nil, err
		}
		out[k] = x
	}
	return out, nil
}

// secrets resolves an image's secret mounts to buildx references: an env
// secret's variable must be set and non-empty (docker inherits it), a src
// secret's expanded path must be a regular file. A missing optional secret
// is dropped with a note; a missing required one fails. Errors and notes
// name the id, variable or path — never a value.
func (e *Engine) secrets(entry Entry) ([]buildx.Secret, error) {
	var out []buildx.Secret
	for _, s := range entry.Image.Secrets {
		if s.Env != "" {
			if v, ok := e.lookupEnv(s.Env); !ok || v == "" {
				if s.Optional {
					e.notef(entry.Name, "build", "secret %s: $%s not set; not mounted", s.ID, s.Env)
					continue
				}
				return nil, fmt.Errorf("secret %q: environment variable %s is not set "+
					"(set it, or mark the secret optional)", s.ID, s.Env)
			}
			out = append(out, buildx.Secret{ID: s.ID, Env: s.Env})
			continue
		}
		src, err := e.expandField("secret "+s.ID+" src", s.Src)
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(src, `,"`) {
			return nil, fmt.Errorf("secret %q: src %q contains , or \" (buildx parses --secret as CSV)", s.ID, src)
		}
		path := e.path(src)
		fi, err := os.Stat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if s.Optional {
				e.notef(entry.Name, "build", "secret %s: %s does not exist; not mounted", s.ID, e.relPath(path))
				continue
			}
			return nil, fmt.Errorf("secret %q: src %s does not exist (create it, or mark the secret optional)",
				s.ID, e.relPath(path))
		case err != nil:
			return nil, fmt.Errorf("secret %q: %w", s.ID, err)
		case !fi.Mode().IsRegular():
			return nil, fmt.Errorf("secret %q: src %s is not a regular file", s.ID, e.relPath(path))
		}
		out = append(out, buildx.Secret{ID: s.ID, Src: path})
	}
	return out, nil
}

// activeAllowlist lints the entry's allowlist against its resolved
// horizon: a malformed or over-horizon entry fails the build; a lapsed
// one is reported and stops suppressing. Returns the entries in force.
func (e *Engine) activeAllowlist(entry Entry, policy spec.ScanPolicy, report *ScanReport) (*spec.Allowlist, error) {
	path := e.AllowlistPath(entry.Name)
	allow, err := spec.LoadAllowlistFile(path)
	if err != nil {
		return nil, err
	}
	today := e.now()
	var violations []string
	for _, issue := range allowlist.Lint(allow, today, policy.Allowlist.MaxDays) {
		if issue.Expired {
			report.Expired = append(report.Expired, issue.ID)
			e.warnf(entry.Name, "scan", "%s; it no longer suppresses anything", issue)
			continue
		}
		violations = append(violations, issue.String())
	}
	if len(violations) > 0 {
		return nil, fmt.Errorf("%s: %s", e.relPath(path), strings.Join(violations, "; "))
	}
	return allowlist.Active(allow, today), nil
}

// buildLayout runs the builder into an OCI layout under work and loads it.
func (e *Engine) buildLayout(ctx context.Context, entry Entry, in buildInputs, work string) (built, error) {
	img := entry.Image
	sde, _ := e.lookupEnv("SOURCE_DATE_EPOCH")
	req := buildx.Request{
		Dockerfile:      e.path(img.DockerfilePath()),
		Context:         e.path(img.ContextDir()),
		Platforms:       entry.Platforms,
		Target:          img.Target,
		Args:            in.args,
		Secrets:         in.secrets,
		Output:          filepath.Join(work, "oci"),
		SourceDateEpoch: sde,
	}
	e.notef(entry.Name, "build", "building %s for %s", e.relPath(req.Dockerfile), strings.Join(req.Platforms, ", "))
	if err := e.builder.Build(ctx, req); err != nil {
		return built{}, err
	}
	b, err := loadBuilt(req.Output)
	if err != nil {
		return built{}, err
	}
	e.notef(entry.Name, "build", "built %s", b.digest)
	return b, nil
}

// scanImage runs the vulnerability scan over every platform image and
// applies the resolved policy and the allowlist.
func (e *Engine) scanImage(ctx context.Context, entry Entry, b built, policy spec.ScanPolicy,
	allow *spec.Allowlist, work string, report *ScanReport) error {
	if !policy.Enabled {
		report.Skipped = "scanning disabled (" + disabledBy(policy) + ")"
		e.notef(entry.Name, "scan", "%s", report.Skipped)
		return nil
	}
	findings, platforms, err := e.collectFindings(ctx, entry, b, work)
	if err != nil {
		return err
	}
	report.Scanned = platforms
	kept, suppressed := scan.Suppress(scan.Blocking(findings, policy), allow)
	report.Blocking = kept
	report.Suppressed = len(suppressed)
	for _, f := range kept {
		e.warnf(entry.Name, "scan", "blocking: %s %s (%s %s, fixed in %v)",
			f.ID, f.Severity, f.Package, f.Installed, f.FixedIn)
	}
	return nil
}

// disabledBy names the switch that turned a resolved policy off.
func disabledBy(policy spec.ScanPolicy) string {
	if policy.Auto {
		return "scan.enabled: false in the patchy config"
	}
	return "the entry's scan.enabled: false"
}

// scanners builds the enabled archive-scanner roster.
func (e *Engine) scanners() []scan.ArchiveScanner {
	if e.archiveScanners != nil {
		return e.archiveScanners
	}
	var out []scan.ArchiveScanner
	if e.global.Scan.Scanners.OSVEnabled() {
		out = append(out, &scan.OSV{Registry: e.reg, Runner: e.tools})
	}
	if e.global.Scan.Scanners.GrypeEnabled() {
		out = append(out, &scan.Grype{Runner: e.tools})
	}
	return out
}

// collectFindings exports each platform image to a docker archive and runs
// every enabled scanner over it, deduplicating findings across platforms.
func (e *Engine) collectFindings(ctx context.Context, entry Entry, b built,
	work string) ([]scan.Finding, []string, error) {
	scanners := e.scanners()
	if len(scanners) == 0 {
		return nil, nil, errors.New("no image scanner enabled: enable scan.scanners.osv or scan.scanners.grype " +
			"in the patchy config (or set scan.enabled: false)")
	}
	images, err := b.platformImages()
	if err != nil {
		return nil, nil, err
	}
	var all []scan.Finding
	var platforms []string
	seen := map[string]int{}
	for i, pi := range images {
		archive := filepath.Join(work, fmt.Sprintf("scan-%d.tar", i))
		if err := pi.saveArchive(archive); err != nil {
			return nil, nil, err
		}
		for _, s := range scanners {
			e.notef(entry.Name, "scan", "scanning %s (%s)", pi.platform, s.Name())
			findings, err := s.ScanArchive(ctx, archive)
			if err != nil {
				return nil, nil, fmt.Errorf("%s scan of %s: %w", s.Name(), pi.platform, err)
			}
			for _, f := range findings {
				// Platforms (and binaries within one image) repeat the same
				// finding; keep one, but union where it was found so the
				// allowlist still names every binary carrying it.
				key := strings.Join([]string{f.Scanner, f.ID, f.Package, f.Installed}, "\x00")
				if i, ok := seen[key]; ok {
					all[i].Locations = scan.MergeLocations(all[i].Locations, f.Locations)
					continue
				}
				seen[key] = len(all)
				all = append(all, f)
			}
		}
		_ = os.Remove(archive)
		platforms = append(platforms, pi.platform)
	}
	return all, platforms, nil
}

// buildManifests runs the Flux manifests flow: expand the annotations,
// stage the filtered tree, sweep it with kubescape, package and publish.
func (e *Engine) buildManifests(ctx context.Context, entry Entry, tags []string, regs []spec.Registry,
	dryRun bool, res *Result) error {
	m := entry.Manifests
	source, err := e.expandField("source", m.Source)
	if err != nil {
		return err
	}
	revision, err := e.expandField("revision", m.Revision)
	if err != nil {
		return err
	}
	work, cleanup, err := workspace()
	if err != nil {
		return err
	}
	defer cleanup()
	staged := filepath.Join(work, "tree")
	e.notef(entry.Name, "build", "staging %s", m.Path)
	if err := fluxartifact.Stage(e.path(m.Path), staged, m.Ignore); err != nil {
		return err
	}
	report := &ScanReport{}
	res.Scan = report
	if err := e.configScan(ctx, entry, staged, report); err != nil {
		return err
	}
	if report.Failed() {
		return errors.New("configuration scan failed (scan.scanners.kubescape.mode: fail); nothing pushed")
	}
	img, err := fluxartifact.Package(staged, map[string]string{
		fluxartifact.SourceAnnotation:   source,
		fluxartifact.RevisionAnnotation: revision,
	})
	if err != nil {
		return err
	}
	digest, err := img.Digest()
	if err != nil {
		return err
	}
	res.Digest = digest.String()
	e.notef(entry.Name, "build", "packaged %s", digest)
	res.Records, err = e.publish(ctx, entry, built{image: img, digest: digest}, tags, regs, false, dryRun)
	return err
}

// configScan sweeps the staged manifests with kubescape: the only scan a
// manifests entry gets, under auto as under true.
func (e *Engine) configScan(ctx context.Context, entry Entry, staged string, report *ScanReport) error {
	policy := spec.EffectiveScanPolicy(e.global.Scan, entry.Scan)
	switch {
	case !policy.Enabled:
		report.ConfigScan = "skipped: scanning disabled (" + disabledBy(policy) + ")"
		e.notef(entry.Name, "scan", "%s", strings.TrimPrefix(report.ConfigScan, "skipped: "))
		return nil
	case !e.global.Scan.Scanners.KubescapeEnabled():
		report.ConfigScan = "skipped: scan.scanners.kubescape is disabled"
		return nil
	}
	mode := e.global.Scan.Scanners.KubescapeMode()
	e.notef(entry.Name, "scan", "scanning manifests configuration (mode: %s)", mode)
	res, err := scan.Kubescape(ctx, e.tools, staged, mode)
	if err != nil {
		return err
	}
	switch {
	case res.Skipped != "":
		report.ConfigScan = "skipped: " + res.Skipped
		e.warnf(entry.Name, "scan", "configuration scan skipped: %s", res.Skipped)
	case res.Failed:
		report.ConfigScan = "failed"
		report.ConfigScanFailed = true
	default:
		report.ConfigScan = "clean"
	}
	return nil
}

// publish converges every selected registry onto the build: per tag, push
// when absent, skip when it already holds this digest, and never move a
// tag holding another digest. Each registry where some tag holds the
// digest then gets it signed under that registry's signing config.
func (e *Engine) publish(ctx context.Context, entry Entry, b built, tags []string, regs []spec.Registry,
	recursive, dryRun bool) ([]Record, error) {
	digest := b.digest.String()
	var recs []Record
	for _, reg := range regs {
		repo := strings.TrimSuffix(reg.URL, "/") + "/" + entry.Repository
		first, holds := len(recs), false
		for _, tag := range tags {
			ref := repo + ":" + tag
			rec := Record{Registry: reg.Name, Ref: ref, Digest: digest}
			if dryRun {
				rec.Action = ActionWouldPush
				e.notef(entry.Name, "publish", "would push %s", ref)
				recs = append(recs, rec)
				continue
			}
			current, exists, err := e.reg.Exists(ctx, ref)
			if err != nil {
				return recs, err
			}
			switch {
			case exists && current == digest:
				rec.Action = ActionSkippedCurrent
				holds = true
				e.notef(entry.Name, "publish", "up to date: %s", ref)
			case exists:
				rec.Action = ActionSkippedTagExists
				rec.Digest = current
				e.warnf(entry.Name, "publish", "%s already points at %s; tags never move, so it was left alone",
					ref, current)
			default:
				e.notef(entry.Name, "publish", "pushing %s", ref)
				if err := b.write(ctx, e.reg, ref); err != nil {
					return recs, err
				}
				rec.Action = ActionPushed
				holds = true
			}
			recs = append(recs, rec)
		}
		if !holds {
			continue
		}
		subject := repo + "@" + digest
		signing := reg.EffectiveSigning(&e.global.Signing)
		if e.ensurer.Valid(ctx, subject, signing) {
			continue
		}
		e.notef(entry.Name, "sign", "signing %s", subject)
		if err := e.ensurer.Sign(ctx, subject, signing, recursive); err != nil {
			return recs, fmt.Errorf("sign %s: %w", subject, err)
		}
		for i := first; i < len(recs); i++ {
			if recs[i].Action != ActionSkippedTagExists {
				recs[i].Signed = true
			}
		}
	}
	return recs, nil
}

// UpdateAllowlist rebuilds and rescans an image entry and regenerates its
// security/allowlists/<name>.yaml from the blocking findings, for entries
// whose resolved scan.allowlist.generate is true. It never pushes or signs
// and never fails on findings: reviewing the allowlist diff in the PR is
// where the risk gets accepted.
func (e *Engine) UpdateAllowlist(ctx context.Context, name string) (*AllowlistResult, error) {
	entry, err := e.Entry(name)
	if err != nil {
		return nil, err
	}
	res := &AllowlistResult{Name: entry.Name}
	skip := func(note string) (*AllowlistResult, error) {
		res.Note = note
		e.notef(entry.Name, "allowlist", "skipping: %s", note)
		return res, nil
	}
	if entry.Kind != KindImage {
		return skip("manifests entries have no allowlist")
	}
	policy := spec.EffectiveScanPolicy(e.global.Scan, entry.Scan)
	if !policy.Allowlist.Generate {
		return skip("scan.allowlist.generate is false")
	}
	if !policy.Enabled {
		return skip("scanning disabled (" + disabledBy(policy) + ")")
	}
	in, err := e.buildInputs(entry)
	if err != nil {
		return res, err
	}
	work, cleanup, err := workspace()
	if err != nil {
		return res, err
	}
	defer cleanup()
	b, err := e.buildLayout(ctx, entry, in, work)
	if err != nil {
		return res, err
	}
	findings, _, err := e.collectFindings(ctx, entry, b, work)
	if err != nil {
		return res, err
	}
	path := e.AllowlistPath(entry.Name)
	prev, err := spec.LoadAllowlistFile(path)
	if err != nil {
		return res, err
	}
	stats, err := allowlist.Regenerate(path, allowlist.BuildBanner, allowlist.Rows(scan.Blocking(findings, policy)),
		prev, e.now(), policy.Allowlist.NewDays, policy.Allowlist.Preamble)
	if err != nil {
		return res, err
	}
	for _, r := range stats.Renewed {
		e.notef(entry.Name, "allowlist", "renewing %s (expired on %s)", r.ID, r.ExpiredAt)
		res.Renewed = append(res.Renewed, r.ID)
	}
	sort.Strings(stats.Dropped)
	for _, id := range stats.Dropped {
		e.notef(entry.Name, "allowlist", "dropping %s (no longer reported)", id)
	}
	res.Path = e.relPath(path)
	res.Generated, res.Kept, res.Added, res.Dropped = true, stats.Kept, stats.Added, stats.Dropped
	e.notef(entry.Name, "allowlist", "wrote %s (%d kept, %d renewed, %d new, %d dropped)",
		res.Path, stats.Kept, len(stats.Renewed), stats.Added, len(stats.Dropped))
	return res, nil
}

// notef emits a note event.
func (e *Engine) notef(entry, stage, format string, args ...any) {
	e.onEvent(Event{Kind: "note", Entry: entry, Stage: stage, Message: fmt.Sprintf(format, args...)})
}

// warnf emits a warning event.
func (e *Engine) warnf(entry, stage, format string, args ...any) {
	e.onEvent(Event{Kind: "warn", Entry: entry, Stage: stage, Message: fmt.Sprintf(format, args...)})
}
