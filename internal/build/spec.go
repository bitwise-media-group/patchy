// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package build

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// The build entry kinds.
const (
	// KindImage is a Dockerfile build published as an OCI image (index).
	KindImage = "image"
	// KindManifests is a Flux manifests directory published as a Flux OCI
	// artifact.
	KindManifests = "manifests"
)

// AllowlistDir is where image entries' allowlists live, relative to the
// working directory: <AllowlistDir>/<name>.yaml.
const AllowlistDir = "security/allowlists"

// Spec is the patchy config's build: block — the first-party artifacts
// patchy build produces, scans, publishes and signs.
type Spec struct {
	// Architectures is the default platform list for image entries
	// (amd64 means linux/amd64; os/arch[/variant] is kept verbatim).
	Architectures []string `yaml:"architectures"`
	// Tags is the default tag template list; an entry's own list replaces
	// it. Templates expand ${ENV} references at build time.
	Tags      []string    `yaml:"tags"`
	Images    []Image     `yaml:"images"`
	Manifests []Manifests `yaml:"manifests"`
	// Policy restricts the registries image builds may pull from. The CLI
	// resolves it (with every enforced layer's) into spec.Config.Policy,
	// which is what the engine enforces; this field is only its YAML home.
	Policy *spec.BuildPolicy `yaml:"policy"`
}

// Image is one Dockerfile build.
type Image struct {
	// Name is the entry's identity: CLI selection, results, and the
	// allowlist file security/allowlists/<name>.yaml. Unique across images
	// and manifests.
	Name string `yaml:"name"`
	// Repository is the path published under every registry URL:
	// <registry.url>/<repository>:<tag>.
	Repository string `yaml:"repository"`
	// Dockerfile is relative to the working directory (default
	// <context>/Dockerfile).
	Dockerfile string `yaml:"dockerfile"`
	// Context is the build context, relative to the working directory
	// (default ".").
	Context string `yaml:"context"`
	// Target optionally names the build stage.
	Target string `yaml:"target"`
	// Architectures overrides the build-level default.
	Architectures []string `yaml:"architectures"`
	// Args are build args; values expand ${ENV}, keys keep their case.
	Args map[string]string `yaml:"args"`
	// Secrets are BuildKit secret mounts (RUN --mount=type=secret,id=…).
	// Each names where the value lives — an environment variable or a
	// file — so the value never reaches the config, the argv or the image.
	Secrets []Secret `yaml:"secrets"`
	// Tags overrides the build-level default list.
	Tags []string `yaml:"tags"`
	// Scan overrides the config's scan block for this image.
	Scan spec.ScanOverride `yaml:"scan"`
}

// Secret is one BuildKit secret an image build mounts. Exactly one of Env
// or Src is set.
type Secret struct {
	// ID is what the Dockerfile mounts: RUN --mount=type=secret,id=<id>.
	// Unique within the image.
	ID string `yaml:"id"`
	// Env names the environment variable holding the value; docker reads
	// it from patchy's environment. A name, never expanded.
	Env string `yaml:"env"`
	// Src is the file holding the value, relative to the working
	// directory; it expands ${ENV} at build time.
	Src string `yaml:"src"`
	// Optional drops the secret (with a note) when its variable is unset
	// or its file is missing, instead of failing the build — for
	// Dockerfiles that mount it with required=false.
	Optional bool `yaml:"optional"`
}

// Manifests is one Flux manifests directory, published as-is.
type Manifests struct {
	// Name is the entry's identity, unique across images and manifests.
	Name string `yaml:"name"`
	// Repository is the path published under every registry URL.
	Repository string `yaml:"repository"`
	// Path is the directory packaged, relative to the working directory.
	Path string `yaml:"path"`
	// Ignore lists gitignore-syntax patterns excluded from the artifact
	// (.git/ is always excluded).
	Ignore []string `yaml:"ignore"`
	// Source expands into the org.opencontainers.image.source annotation.
	Source string `yaml:"source"`
	// Revision expands into the org.opencontainers.image.revision
	// annotation (Flux's <ref>@sha1:<sha> convention).
	Revision string `yaml:"revision"`
	// Tags overrides the build-level default list.
	Tags []string `yaml:"tags"`
	// Scan sets only enabled here: a manifests entry gets the kubescape
	// configuration scan, and the image-scan fields are rejected.
	Scan spec.ScanOverride `yaml:"scan"`
}

// Entry is one build entry of either kind, resolved against the build-level
// defaults.
type Entry struct {
	Name       string
	Kind       string
	Repository string
	// Tags are the unexpanded tag templates (entry over build default).
	Tags []string
	// Scan is the entry's own scan block.
	Scan spec.ScanOverride
	// Image is set for KindImage, Manifests for KindManifests.
	Image     *Image
	Manifests *Manifests
	// Platforms is the normalized platform list (images only).
	Platforms []string
}

// Entries lists every entry, images first, in declaration order.
func (s *Spec) Entries() []Entry {
	out := make([]Entry, 0, len(s.Images)+len(s.Manifests))
	for i := range s.Images {
		img := &s.Images[i]
		archs := img.Architectures
		if len(archs) == 0 {
			archs = s.Architectures
		}
		out = append(out, Entry{
			Name: img.Name, Kind: KindImage, Repository: img.Repository,
			Tags: pickTags(img.Tags, s.Tags), Scan: img.Scan, Image: img,
			Platforms: Platforms(archs),
		})
	}
	for i := range s.Manifests {
		m := &s.Manifests[i]
		out = append(out, Entry{
			Name: m.Name, Kind: KindManifests, Repository: m.Repository,
			Tags: pickTags(m.Tags, s.Tags), Scan: m.Scan, Manifests: m,
		})
	}
	return out
}

// Entry looks one entry up by name.
func (s *Spec) Entry(name string) (Entry, bool) {
	for _, e := range s.Entries() {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// pickTags resolves an entry's tag list: its own when set, else the default.
func pickTags(entry, def []string) []string {
	if len(entry) > 0 {
		return entry
	}
	return def
}

// Platforms normalizes an architectures list: a bare architecture means
// linux/<arch>; anything with a slash (os/arch[/variant]) is kept verbatim.
func Platforms(archs []string) []string {
	out := make([]string, 0, len(archs))
	for _, a := range archs {
		if strings.Contains(a, "/") {
			out = append(out, a)
		} else {
			out = append(out, "linux/"+a)
		}
	}
	return out
}

// DockerfilePath resolves an image's Dockerfile (default <context>/Dockerfile).
func (i *Image) DockerfilePath() string {
	if i.Dockerfile != "" {
		return i.Dockerfile
	}
	return filepath.Join(i.ContextDir(), "Dockerfile")
}

// ContextDir resolves an image's build context (default ".").
func (i *Image) ContextDir() string {
	if i.Context != "" {
		return i.Context
	}
	return "."
}

// Validation patterns. Entry names become file names (the allowlist) and
// CLI arguments; repositories follow the OCI distribution path grammar.
var (
	nameRe       = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)
	repositoryRe = regexp.MustCompile(`^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*$`)
	platformRe   = regexp.MustCompile(`^[a-z0-9_]+(/[a-z0-9_]+(/[a-z0-9_]+)?)?$`)
	// secretIDRe keeps ids free of the , and = buildx's CSV --secret
	// spec splits on.
	secretIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	envNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Validate checks the build block against the config's scan defaults:
// names (well-formed, unique across both kinds), repositories, platforms,
// manifests paths, and each entry's scan block — including the resolved
// allowlist horizon for images, and the image-only fields a manifests
// entry may not set.
func (s *Spec) Validate(global spec.Scan) error {
	seen := map[string]string{}
	claim := func(prefix, name string) error {
		if !nameRe.MatchString(name) {
			return fmt.Errorf("%s: name %q must match %s (it names the allowlist file and CLI selection)",
				prefix, name, nameRe)
		}
		if other, ok := seen[name]; ok {
			return fmt.Errorf("%s: duplicate name %q (also %s)", prefix, name, other)
		}
		seen[name] = prefix
		return nil
	}
	for i, img := range s.Images {
		prefix := fmt.Sprintf("build.images[%d]", i)
		if err := claim(prefix, img.Name); err != nil {
			return err
		}
		if err := s.validateImage(fmt.Sprintf("%s (%s)", prefix, img.Name), img, global); err != nil {
			return err
		}
	}
	for i, m := range s.Manifests {
		prefix := fmt.Sprintf("build.manifests[%d]", i)
		if err := claim(prefix, m.Name); err != nil {
			return err
		}
		prefix = fmt.Sprintf("%s (%s)", prefix, m.Name)
		if err := validateRepository(prefix, m.Repository); err != nil {
			return err
		}
		if m.Path == "" {
			return fmt.Errorf("%s: path is required", prefix)
		}
		if err := validateTags(prefix, m.Tags); err != nil {
			return err
		}
		if err := validateScan(prefix, m.Scan); err != nil {
			return err
		}
		if m.Scan.HasImageFields() {
			return fmt.Errorf("%s: scan: failOn, ignoreUnfixed and allowlist apply to image scans only "+
				"(a manifests entry gets the kubescape configuration scan; set only scan.enabled)", prefix)
		}
	}
	return validateTags("build", s.Tags)
}

// validateImage checks one image entry past its name: repository,
// platforms (entry over build default), secrets, tags and scan block.
func (s *Spec) validateImage(prefix string, img Image, global spec.Scan) error {
	if err := validateRepository(prefix, img.Repository); err != nil {
		return err
	}
	archs := img.Architectures
	if len(archs) == 0 {
		archs = s.Architectures
	}
	if len(archs) == 0 {
		return fmt.Errorf("%s: no architectures: set build.architectures or the entry's own", prefix)
	}
	for _, a := range archs {
		if !platformRe.MatchString(a) {
			return fmt.Errorf("%s: architecture %q is not arch or os/arch[/variant]", prefix, a)
		}
	}
	if err := validateSecrets(prefix, img.Secrets); err != nil {
		return err
	}
	if err := validateTags(prefix, img.Tags); err != nil {
		return err
	}
	if err := validateScan(prefix, img.Scan); err != nil {
		return err
	}
	if err := spec.EffectiveScanPolicy(global, img.Scan).Allowlist.Validate(); err != nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return nil
}

// validateRepository checks one publish repository path.
func validateRepository(prefix, repo string) error {
	if repo == "" {
		return fmt.Errorf("%s: repository is required", prefix)
	}
	if !repositoryRe.MatchString(repo) {
		return fmt.Errorf("%s: repository %q is not a valid OCI repository path", prefix, repo)
	}
	return nil
}

// validateSecrets checks an image's secret mounts: well-formed unique ids,
// and exactly one well-formed source each. src stays a template until
// build time, like the other templated fields.
func validateSecrets(prefix string, secrets []Secret) error {
	seen := map[string]bool{}
	for i, s := range secrets {
		at := fmt.Sprintf("%s: secrets[%d]", prefix, i)
		switch {
		case s.ID == "":
			return fmt.Errorf("%s: id is required", at)
		case !secretIDRe.MatchString(s.ID):
			return fmt.Errorf("%s: id %q must match %s", at, s.ID, secretIDRe)
		case seen[s.ID]:
			return fmt.Errorf("%s: duplicate id %q", at, s.ID)
		}
		seen[s.ID] = true
		at = fmt.Sprintf("%s (%s)", at, s.ID)
		switch {
		case (s.Env == "") == (s.Src == ""):
			return fmt.Errorf("%s: set exactly one of env or src", at)
		case s.Env != "" && !envNameRe.MatchString(s.Env):
			return fmt.Errorf("%s: env %q is not an environment variable name (it is a name, not a template)",
				at, s.Env)
		case s.Src != "" && strings.TrimSpace(s.Src) == "":
			return fmt.Errorf("%s: src is blank", at)
		}
	}
	return nil
}

// validateTags rejects empty templates; the expanded tags are checked
// against the OCI grammar at build time.
func validateTags(prefix string, tags []string) error {
	for i, t := range tags {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("%s: tags[%d] is empty", prefix, i)
		}
	}
	return nil
}

// validateScan checks the enabled switch.
func validateScan(prefix string, o spec.ScanOverride) error {
	if !o.Enabled.Valid() {
		return fmt.Errorf("%s: scan.enabled must be auto, true or false (got %q)", prefix, o.Enabled)
	}
	return nil
}
