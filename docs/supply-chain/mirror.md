# Mirroring charts and images

`patchy mirror` maintains a **mirror store**: a git repository in which every `charts/<name>/` or `artifacts/<name>/`
directory pins one upstream helm chart or OCI artifact. Each entry is vendored so the pull request shows exactly what
changed upstream, pinned by digest, checked against its publisher's signatures, scanned, and published to your own
registries signed with your identity. Clusters then pull only from the mirror, so nothing reaches them without passing
through review.

This page walks through building and running a store. For every flag, see the
[command reference](../cli/patchy_mirror.md).

## The store

The tree is the registry of entries: patchy discovers `charts/*/manifest.yaml` and `artifacts/*/manifest.yaml`, and
nothing else lists them.

```text
patchy.yaml                     # registries, signing, scan defaults, mirror: settings
charts/
  cert-manager/
    manifest.yaml               # intent: what to mirror and how (you edit this)
    values/discovery.yaml       # intent: values the discovery render uses
    vendor/cert-manager/        # fact: the upstream chart, unpacked
    rendered/manifests.yaml     # fact: the chart rendered with the discovery values
    images.lock.yaml            # fact: every image, by digest, with its publish targets
    images.extra.yaml           # fact: images derived from distribution manifests (when configured)
    security/allowlist.yaml     # accepted findings (generated when scan.allowlist.generate is on)
artifacts/
  runner-bundle/
    manifest.yaml               # intent
    lock.yaml                   # fact: the artifact's digest, platforms and targets
    security/allowlist.yaml
```

**Intent** is what a person decides and a reviewer approves. **Fact** is everything derived from it, which only the
machine writes. `upgrade` regenerates fact from intent. `validate` regenerates it again out-of-tree and byte-compares
the result with what is committed, so a hand edit to a fact file, or an intent change committed without running
`upgrade`, fails the gate.

The store root is the working directory, or `-C` / `mirror.directory` relative to it. The
[patchy config](../cli.md#configuration) is always found from the working directory, and its `mirror:` block holds the
store-wide settings:

```yaml
mirror:
  update:
    # Soak period for tracked image tags and artifact versions: a tag published more recently than this can still be
    # yanked or hot-fixed. Keep it short, since the soak also delays fixes.
    cooldownDays: 3
  # Rewrites applied when PULLING from upstream (a pull-through cache in front of docker.io, say). Recorded sources and
  # publish targets keep the canonical path.
  sourceRegistryRewrites:
    docker.io: mirror.gcr.io
```

## Adding a chart

```sh
patchy mirror add dex --url https://charts.dexidp.io
```

`add` works out what the URL points at (an `https://` helm repository, an `oci://` chart repository, or a bare artifact
reference), pins the newest stable version, sets the constraint to stay within that major version, and writes
`charts/dex/manifest.yaml`. In a directory with no patchy config yet, it also writes a starter `patchy.yaml` with
commented defaults. Nothing is downloaded and nothing is committed.

Edit the scaffold (at minimum, declare how upstream signs its images), then vendor and lock it:

```sh
patchy mirror upgrade dex
git add charts/dex && git commit -m "feat(dex): mirror the dex chart"
```

The pull request now shows the vendored chart, the rendered manifests, and the exact image digests that will be
mirrored.

## The chart manifest

```yaml
apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: cert-manager # must match the directory name
chart:
  repo: oci://quay.io/jetstack/charts # oci:// registry path or https:// helm repository
  name: cert-manager
  version: "v1.21.2" # the pin; upgrade splices it
  versionConstraint: ">=1.18.0 <2.0.0" # what upgrade may pick
  # lockstep: flux                     # bump together with every entry naming the same group
  verifyUpstream:
    provider: none # the chart artifact carries no signature: a documented gap
discovery:
  namespace: cert-manager # render namespace (default: the entry name)
  valuesFiles: [values/discovery.yaml] # relative to the entry directory
  kubeVersion: "1.34.0" # .Capabilities.KubeVersion (default 1.34.0)
  # apiVersions: [monitoring.coreos.com/v1]   # extra .Capabilities.APIVersions
images:
  extra:
    - image: "quay.io/jetstack/cert-manager-acmesolver:{appVersion}"
      reason: launched per challenge from a controller flag, never in a pod spec
  exclude: []
  verifyUpstream:
    - match: "quay.io/jetstack/*" # glob over canonical source references; first match wins
      provider: cosign-key
      key: "https://cert-manager.io/public-keys/cert-manager-pubkey-2021-09-20.pem"
      signatureDigestAlgorithm: sha512
scan:
  allowlist:
    generate: true # upgrade transcribes blocking findings into security/allowlist.yaml
publish:
  chartRepo: charts/cert-manager # default <chartNamespace>/<name>
```

The pin must be spelled exactly as the chart's own `Chart.yaml` version: `sync` publishes the chart tagged with the
pin, and `helm push` rejects a tag that differs from the chart version. cert-manager versions its chart `v1.21.2`, so
the pin carries the `v`. `upgrade` and `validate` fail on a mismatch and name the pin to use. When upstream tags a
release under both spellings, `upgrade` keeps the one the current pin uses.

### Provenance: `verifyUpstream`

Every locked image must match one `images.verifyUpstream` rule, and the chart has its own `chart.verifyUpstream`.
`validate` and `sync` check each artifact's upstream signature with cosign before anything is trusted.

| Provider         | Fields                                                                                              | Use for                                                  |
| ---------------- | --------------------------------------------------------------------------------------------------- | -------------------------------------------------------- |
| `cosign-keyless` | `certificateIdentityRegexp` (required), `certificateOidcIssuer` (default the GitHub Actions issuer) | projects that sign from CI with Fulcio                   |
| `cosign-key`     | `key` (URL or path, required), `signatureDigestAlgorithm`, `signatureRepository`                    | projects that publish a cosign public key                |
| `none`           | —                                                                                                   | a documented gap: the publisher signs nothing verifiable |

`provider: none` is allowed so the gap is written down and reviewed, rather than skipped silently. A comment saying why
is the convention.

### Image discovery

patchy finds images by rendering the chart (with `discovery.valuesFiles`) and reading the output, in four passes:

1. Pod-spec container lists (`containers`, `initContainers`, `ephemeralContainers`) at any depth.
2. Operator-style top-level `spec.image` values.
3. A conservative sweep of ConfigMap data for image-shaped strings, which catches embedded pod templates.
4. The manifest's `images.extra`, for images no render can see (`{appVersion}` expands from the vendored `Chart.yaml`).

References are normalized the way container runtimes expand shorthand (`nginx` becomes `docker.io/library/nginx`),
filtered through `images.exclude` globs, and resolved to digests. A chart that renders no images (CRDs only) needs
`images.allowEmpty: true`.

Two features cover images that discovery cannot reach:

- **`images.distributionManifests`** handles operators that deploy workloads from manifests helm never renders. patchy
  pulls the named manifests artifact (`{appVersion}` expands), reads the Deployment images of the listed components, and
  writes them to the generated `images.extra.yaml` sidecar.
- **`images.track`** handles an image on a release train of its own (a runner image bumped more often than the chart).
  `upgrade` re-picks its tag within `versionConstraint` (by default the pinned tag's own major version), honouring the
  cooldown, and writes the pick into a values file at `valuesPath`. That value must already exist; the pick replaces it.

```yaml
images:
  track:
    - image: ghcr.io/actions/actions-runner
      versionConstraint: ">=2.320.0 <3.0.0"
      valuesPath: .template.spec.containers[0].image # a full image reference, or a bare tag
      # valuesFile: values/discovery.yaml           # default: the first discovery.valuesFiles entry
      # cooldownDays: 0                             # 0 tracks head with no soak
```

### Keeping rendered output inside the mirror

`lint` refuses an `oci://` reference in the rendered output that points outside your registries: it would be a pull the
platform makes at runtime that skips the review this store exists for. An escape needs a reasoned allowance:

```yaml
ociRefs:
  allow:
    - pattern: "oci://ghcr.io/acme/policies/*"
      reason: signed policy bundles verified by the admission controller
```

## Mirroring an artifact

An artifact entry pins one OCI artifact that is not a helm chart: a manifests bundle, a policy bundle, or a standalone
image.

```sh
patchy mirror add runner-bundle --url ghcr.io/acme/runner-bundle --type artifact
```

```yaml
apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Artifact
name: runner-bundle
artifact:
  ref: ghcr.io/acme/runner-bundle # the repository, no tag
  version: "1.8.0" # the pinned tag
  versionConstraint: ">=1.0.0 <2.0.0"
  # cooldownDays: 7                  # soak for version picks (default mirror.update.cooldownDays)
  verifyUpstream:
    provider: cosign-keyless
    certificateIdentityRegexp: "^https://github.com/acme/runner-bundle/"
scan:
  enabled: auto # scan only if the artifact is a runnable image
publish:
  repo: artifacts/ghcr.io/acme/runner-bundle # default <artifactNamespace>/<canonical source path>
```

Under `scan.enabled: auto` an artifact is scanned only when it is a runnable image. Its `failOn`, `ignoreUnfixed` and
`allowlist` work exactly as a chart's.

## Lockstep groups

Charts released together upstream, such as an operator and its CRD chart, must not be mirrored at different versions.
Give them the same `chart.lockstep` (or `artifact.lockstep`) value. `upgrade` then targets the **lowest** of the
members' newest satisfying versions, holding the bump until upstream has published every member, so a half-mirrored
release never merges. `--group` selects a whole group: `patchy mirror upgrade --group flux --to 0.57.0`.

## The lifecycle

| Verb               | Touches          | What it does                                                                                                                                                                       |
| ------------------ | ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `upgrade`          | the working tree | resolves the newest version satisfying each constraint, re-picks tracked tags past their cooldown, re-vendors, re-renders, re-locks and (with `generate`) re-derives the allowlist |
| `upgrade --check`  | nothing          | the same plan, reported as data                                                                                                                                                    |
| `upgrade --pinned` | the working tree | converges on pins someone else moved (Renovate): picks nothing, but every pin must satisfy its constraint                                                                          |
| `validate`         | nothing          | regenerates out-of-tree and byte-compares, verifies provenance, scans, lints                                                                                                       |
| `sync`             | your registries  | publishes the committed state, idempotently                                                                                                                                        |

```sh
patchy mirror upgrade --check -o json        # what would move, as data
patchy mirror upgrade --all                  # move every pin, regenerate everything derived
patchy mirror validate --all                 # the gate
patchy mirror validate dex --only regen,lint # just the cheap stages
patchy mirror sync --all --dry-run           # what publishing would do
patchy mirror sync --all                     # converge every registry
```

`upgrade` is the only verb that consults the wall clock (tracked-tag cooldowns, allowlist expiry stamps). Keeping the
clock out of `validate` is what makes its byte comparison deterministic between a commit and the CI run that checks it.

`validate` runs four stages, selectable with `--only`:

- **regen**: vendor, render, discover and lock out-of-tree, then demand byte-identity with the commit.
- **verify**: check upstream provenance for the chart and every locked image.
- **scan**: scan the locked images and, with kubescape enabled, the rendered manifests. See
  [Scanning and allowlists](scanning.md).
- **lint**: schema-valid files can still break policy: every image has a provenance rule, tracked images resolve,
  `oci://` references stay inside the mirror, and the allowlist has statements and expiry dates within the horizon.

`sync` re-pulls each upstream chart and fails loudly if upstream changed a released version (digest against the lock,
tree against the committed `vendor/`). Then, for each registry, it pushes the chart unless its tag already exists (an
existing tag is never replaced), copies every locked image by digest, and signs everything it published that does not
already carry a valid signature. Skips are successes, so a re-run after a partial failure finishes the rest. `--workers`
(default 4) bounds concurrent registry operations per stage.

## Running a store in CI

The reference store, bitwise-media-group/flux-containers, runs four workflows. Each one is a few lines of shell around
patchy, and the markdown report is the human-facing output. These are the steps that matter, with checkout, tool
installation and registry login trimmed.

**Pull request: validate only what changed.** A config change affects every entry.

```sh
changed="$(git diff --name-only "origin/$BASE_REF"...HEAD -- charts/ artifacts/ | cut -d/ -f2 | sort -u | xargs)"
git diff --quiet "origin/$BASE_REF"...HEAD -- patchy.yaml || changed="--all"
patchy mirror validate $changed -o markdown > summary.md  # post as a sticky PR comment; the exit code is the gate
```

**Merge to main: publish.** Provenance is checked again on main before anything is signed with the platform identity, so
nothing merged around the PR gate gets the signature.

```sh
patchy mirror validate --all --only verify
patchy mirror sync --all -o markdown >> "$GITHUB_STEP_SUMMARY"
```

The job needs `id-token: write` for keyless signing and a registry login with push rights. Pin the publish workflow to
the default branch: its certificate subject is what your clusters trust.

**Weekly: rescan what is already published.** Vulnerability databases move after a merge. A rescan never blocks
anything; findings become issues, fixed by bumping the chart or by accepting them in the allowlist.

```sh
for dir in charts/*/ artifacts/*/; do
  entry="$(basename "$dir")"
  patchy mirror validate "$entry" --only scan,lint -o markdown > "rescan-$entry.md" || failed="$failed $entry"
done
```

**Daily: open update pull requests.** `upgrade --check -o json` reports the plan as lockstep groups, each with a target
and its members. Give each group one stable branch (`update/<group>`) that is recreated from main and refreshed in
place, so a newer upstream release rewrites the open PR instead of opening another next to it:

```sh
patchy mirror upgrade --check -o json --all > plan.json
jq -c '.groups[]' plan.json | while read -r group; do
  target="$(jq -r .target <<<"$group")"
  jq -r '.members[].name' <<<"$group" | while read -r member; do
    patchy mirror upgrade "$member" --to "$target"
  done
  # commit charts/ and artifacts/ to update/<group> and open or refresh its PR
done
```

## Renovate

If Renovate (or anything else) moves `chart.version` or a tracked tag, run `patchy mirror upgrade --pinned <entry>` in a
post-upgrade task. It regenerates everything derived without picking anything itself, and fails if the new pin breaks
its constraint or splits a lockstep group.

## Migrating from `mirror.yaml`

Stores created before the layered config kept their settings in a `mirror.yaml` at the root. Every `mirror` command now
refuses to run while that file exists, and prints the migration. See
[Migrating from `mirror.yaml`](../cli.md#migrating-from-mirroryaml-and-patchyyaml): drop `apiVersion` and `kind`, keep
`registries`, `signing` and `scan` at the top level of `patchy.yaml`, move `update` and `sourceRegistryRewrites` under
`mirror:`, and rename `scan.allowlistMaxDays` / `allowlistNewDays` to `scan.allowlist.maxDays` / `newDays`. Compare
`patchy mirror validate --all` before and after: the results should be identical.
