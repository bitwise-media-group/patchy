// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"strings"
)

// scaffoldConfig is the starter patchy.yaml mirror add writes when the
// working directory has no patchy config: every value the pipelines need,
// commented for the reviewer to fill in. It must load and validate as-is.
func scaffoldConfig() []byte {
	return []byte(`# patchy configuration, shared by patchy mirror and patchy build. Layers merge
# from /etc/patchy, ~/.config/patchy and the project (this file wins); maps
# merge, lists replace, and null deletes an inherited key.

registries:
  # Where everything publishes: every entry goes to every registry listed
  # here. The name keys the lock files' targets and --registry.
  - name: primary
    url: registry.example.com/org/platform
    # Mirror path prefixes inside the registry, per artifact class.
    chartNamespace: charts
    imageNamespace: images        # target = images/<canonical source path>:<tag>
    artifactNamespace: artifacts
    # signing:                    # replaces the global signing block wholesale
    #   provider: kms             # for pushes to this registry (keyless identity
    #   kms:                      # is the run's ambient OIDC identity — the same
    #     key: gcpkms://projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>
    #                             # for every registry — so per-registry
    #                             # variation means KMS keys)
  # - name: ghcr
  #   url: ghcr.io/org/platform

signing:
  # How published OCI artifacts are signed: the default for registries
  # without their own signing: block. Signatures are sigstore bundles
  # attached via OCI referrers (older ECR private registries fall back to
  # cosign's tag-based scheme).
  provider: keyless               # keyless | kms
  keyless:
    # The exact certificate identity the publish workflow's signatures
    # carry — consumers pin this value.
    certificateIdentity: https://github.com/org/repo/.github/workflows/publish.yaml@refs/heads/main
    certificateOidcIssuer: https://token.actions.githubusercontent.com
    # Public transparency log entry (public repos want the log).
    tlogUpload: true
  # kms:
  #   key: gcpkms://projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>

scan:
  # The defaults every mirror entry and build entry inherits; an entry's own
  # scan: block overrides any field (enabled: auto | true | false there).
  enabled: true
  scanners:
    # shells out to osv-scanner, which must be on PATH (default off)
    osv:
      enabled: true
    # use grype as an alternative (or in addition) to osv
    grype:
      enabled: false
    # scan helm chart and manifest configs
    kubescape:
      enabled: true
      mode: warn
  failOn: [CRITICAL, HIGH]
  ignoreUnfixed: true
  allowlist:
    # Allowlist entries must expire within maxDays; new derived entries get
    # newDays from the day they first appear.
    maxDays: 90
    newDays: 90
    # Regenerate allowlists from fresh scans (mirror upgrade, build
    # --update-allowlists); entries usually opt in individually.
    generate: false

mirror:
  # The mirror store root, relative to the working directory (default: the
  # working directory). Also --directory / PATCHY_MIRROR_DIRECTORY.
  # directory: mirror
  update:
    # Soak window for tracked-tag and artifact-version picks: a tag published
    # this recently can still be yanked or hot-fixed. Kept short on purpose —
    # every day here is a day the platform knowingly runs behind on fixes.
    cooldownDays: 3
  # Rewrites applied when PULLING from an upstream registry (e.g. a mirror in
  # front of docker.io); recorded sources and publish targets keep the
  # canonical path.
  sourceRegistryRewrites: {}

# build:
#   # First-party images and Flux manifest trees: patchy build builds,
#   # scans (before any push), publishes to every registry and signs them.
#   architectures: [amd64, arm64]
#   tags:
#     - "${VERSION}-${CHANNEL}.${PATCHY_TIMESTAMP}.g${GITHUB_SHA:0:7}"
#     - "sha-${GITHUB_SHA}"
#   images:
#     - name: controller           # allowlist: security/allowlists/controller.yaml
#       repository: org/controller
#       dockerfile: Dockerfile
#       context: .
#       args: {GO_VERSION: "1.27"}
#       scan:
#         allowlist: {generate: true}
#   manifests:
#     - name: platform
#       repository: manifests/platform
#       path: deploy/flux
#       ignore: ["*.md"]
#       source: "${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}"
#       revision: "${GITHUB_REF_NAME}@sha1:${GITHUB_SHA}"
`)
}

// scaffoldChartManifest writes a new chart entry's manifest.
func scaffoldChartManifest(name, repo, chartName, version, constraint, lockstep string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: Chart\nname: %s\n", name)
	b.WriteString("chart:\n")
	fmt.Fprintf(&b, "  repo: %s\n", repo)
	fmt.Fprintf(&b, "  name: %s\n", chartName)
	if lockstep != "" {
		fmt.Fprintf(&b, "  lockstep: %s\n", lockstep)
	}
	fmt.Fprintf(&b, "  version: %q\n", version)
	fmt.Fprintf(&b, "  versionConstraint: %q\n", constraint)
	b.WriteString(`  # Declare the chart's upstream provenance; provider: none documents a
  # verification gap rather than hiding it.
  verifyUpstream:
    provider: none
discovery:
`)
	fmt.Fprintf(&b, "  namespace: %s\n", name)
	b.WriteString(`  valuesFiles: [values/discovery.yaml]
  kubeVersion: "1.34.0"
images:
  extra: []
  exclude: []
  # Every locked image must match a rule; first match wins. Replace the
  # catch-all below with real provenance rules where upstream signs.
  verifyUpstream:
    - match: "**"
      provider: none
# publish:
#   # Override the default publish path (<chartNamespace>/<name>), applied
#   # under every registry URL.
#   chartRepo: charts/example
`)
	return []byte(b.String())
}

// scaffoldArtifactManifest writes a new artifact entry's manifest.
func scaffoldArtifactManifest(name, ref, version, constraint, lockstep string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: Artifact\nname: %s\n", name)
	b.WriteString("artifact:\n")
	fmt.Fprintf(&b, "  ref: %s\n", ref)
	if lockstep != "" {
		fmt.Fprintf(&b, "  lockstep: %s\n", lockstep)
	}
	fmt.Fprintf(&b, "  version: %q\n", version)
	fmt.Fprintf(&b, "  versionConstraint: %q\n", constraint)
	b.WriteString(`  # Declare the artifact's upstream provenance; provider: none documents
  # a verification gap rather than hiding it.
  verifyUpstream:
    provider: none
scan:
  # auto scans iff the artifact is a runnable image. Any field of the
  # config's scan block may be overridden here.
  enabled: auto
  # failOn: [CRITICAL]
  # allowlist:
  #   generate: true            # regenerate security/allowlist.yaml on upgrade
  #   maxDays: 30
# publish:
#   # Override the default publish path (<artifactNamespace>/<ref>), applied
#   # under every registry URL.
#   repo: artifacts/example.com/org/thing
`)
	return []byte(b.String())
}
