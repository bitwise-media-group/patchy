// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"strings"
)

// scaffoldMirrorConfig is the starter mirror.yaml written on first use:
// every value the pipeline needs, commented for the reviewer to fill in.
func scaffoldMirrorConfig() []byte {
	return []byte(`apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: MirrorConfig

registry:
  # Where everything publishes: one registry path serving arbitrary
  # sub-paths beneath it.
  url: registry.example.com/org/platform
  # Path prefixes inside the registry, per artifact class.
  chartNamespace: charts
  imageNamespace: images          # target = images/<canonical source path>:<tag>
  artifactNamespace: artifacts

signing:
  # How published OCI artifacts are signed. Default for every entry; a
  # per-entry signing: block replaces this wholesale.
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

update:
  # Soak window for tracked-tag and artifact-version picks: a tag published
  # this recently can still be yanked or hot-fixed. Kept short on purpose —
  # every day here is a day the platform knowingly runs behind on fixes.
  cooldownDays: 3

scan:
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
  # Allowlist entries must expire within this horizon; new derived entries
  # get allowlistNewDays from the day they first appear.
  allowlistMaxDays: 90
  allowlistNewDays: 90

# Rewrites applied when PULLING from an upstream registry (e.g. a mirror in
# front of docker.io); recorded sources and publish targets keep the
# canonical path.
sourceRegistryRewrites: {}
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
    - match: "*"
      provider: none
# publish:
#   # Override the default publish path (<registry.chartNamespace>/<name>).
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
  # auto scans iff the artifact is a runnable image.
  enabled: auto
# publish:
#   # Override the default publish path (<registry.artifactNamespace>/<ref>).
#   repo: artifacts/example.com/org/thing
`)
	return []byte(b.String())
}
