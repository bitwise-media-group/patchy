# Mirroring charts and images

`patchy mirror` maintains a **mirror store**. This is a git repository in which every `charts/<name>/` or
`artifacts/<name>/` directory pins one upstream helm chart or OCI artifact. For each entry, patchy:

- vendors it (copies the upstream files into the repository), so the pull request shows what changed upstream
- pins it by digest
- checks it against its publisher's signatures
- scans it
- publishes it to your own registries, signed with your identity.

Clusters then pull only from the mirror, so nothing reaches them without review.

This page shows how to build and run a store. For every flag, see the [command reference](../cli/patchy_mirror.md).

## The store

patchy finds entries by looking for `charts/*/manifest.yaml` and `artifacts/*/manifest.yaml`. There is no separate list
of entries.

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
machine writes. `upgrade` regenerates fact from intent. `validate` regenerates it again outside the working tree and
byte-compares the result with what is committed. So the gate fails on a hand edit to a fact file, and on an intent
change committed without running `upgrade`.

The store root is the working directory, or `-C` / `mirror.directory` relative to it. patchy always looks for the
[patchy config](configuration.md) from the working directory. Its `mirror:` block holds the settings for the whole
store:

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
  # Where upstream charts, images and artifacts may come from. See Restricting sources.
  policy:
    images:
      allow: ["registry.k8s.io/**", "ghcr.io/fluxcd/*"]
```

`mirror.policy` limits the upstream sources the store may pull from. It is checked before every pull, in `lint`, and
before `sync` pushes anything. See [Restricting sources](source-policy.md).

## Adding a chart

```sh
patchy mirror add dex --url https://charts.dexidp.io
```

`add` works out what the URL points at: an `https://` helm repository, an `oci://` chart repository, or a bare artifact
reference. It pins the newest stable version, sets the constraint to stay within that major version, and writes
`charts/dex/manifest.yaml`. In a directory with no patchy config yet, it also writes a starter `patchy.yaml` with
commented defaults. Nothing is downloaded and nothing is committed.

Edit the new manifest. At a minimum, declare how upstream signs its images. Then vendor and lock the entry:

```sh
patchy mirror upgrade dex
git add charts/dex && git commit -m "feat(dex): mirror the dex chart"
```

The pull request now shows the vendored chart, the rendered manifests, and the image digests that will be mirrored.

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
    - match: "quay.io/jetstack/*" # path glob over canonical source references; first match wins
      provider: cosign-key
      key: "https://cert-manager.io/public-keys/cert-manager-pubkey-2021-09-20.pem"
      signatureDigestAlgorithm: sha512
scan:
  allowlist:
    generate: true # upgrade transcribes blocking findings into security/allowlist.yaml
publish:
  chartRepo: charts/cert-manager # default <chartNamespace>/<name>
```

The pin must match the version in the chart's own `Chart.yaml`, character for character. `sync` publishes the chart
tagged with the pin, and `helm push` rejects a tag that differs from the chart version. cert-manager versions its chart
`v1.21.2`, so the pin carries the `v`. `upgrade` and `validate` fail on a mismatch and name the pin to use. When
upstream tags a release under both spellings, `upgrade` keeps the one the current pin uses.

### Provenance: `verifyUpstream`

Every locked image must match one `images.verifyUpstream` rule, and the chart has its own `chart.verifyUpstream`. patchy
checks upstream signatures with cosign for each subject (the chart, image or artifact being published). It does this in
two places:

- **`validate`'s verify stage** checks every subject. This is the pull-request gate.
- **`sync`** checks each subject just before it first pushes or signs it. So nothing reaches your registries unverified,
  even when `validate` never ran. Each subject is checked once per run, however many registries it publishes to. A copy
  that is already current and signed everywhere is not checked again, so re-syncing an up-to-date store makes no
  upstream calls. `sync --dry-run` reports what it would verify without calling cosign.

A `provider: none` rule is reported as a documented gap in both places and never fails.

`match` is a path glob over the canonical, tagged source (`quay.io/jetstack/cert-manager-controller:v1.21.2`): `*`
matches within one `/`-separated segment and `**` across segments, so `**` is the catch-all. The first matching rule
wins.

| Provider         | Fields                                                                                              | Use for                                                  |
| ---------------- | --------------------------------------------------------------------------------------------------- | -------------------------------------------------------- |
| `cosign-keyless` | `certificateIdentityRegexp` (required), `certificateOidcIssuer` (default the GitHub Actions issuer) | projects that sign from CI with Fulcio                   |
| `cosign-key`     | `key` (URL or path, required), `signatureDigestAlgorithm`, `signatureRepository`                    | projects that publish a cosign public key                |
| `none`           | none                                                                                                | a documented gap: the publisher signs nothing verifiable |

`provider: none` exists so that the gap is written down and reviewed instead of skipped silently. By convention, add a
comment that says why.

### Image discovery

patchy finds images by rendering the chart (with `discovery.valuesFiles`) and reading the output, in four passes:

1. Pod-spec container lists (`containers`, `initContainers`, `ephemeralContainers`) at any depth.
2. Operator-style top-level `spec.image` values.
3. A conservative sweep of ConfigMap data for image-shaped strings, which catches embedded pod templates.
4. The manifest's `images.extra`, for images no render can see (`{appVersion}` expands from the vendored `Chart.yaml`).

patchy then processes the references it found:

1. It normalizes each one the way container runtimes expand shorthand (`nginx` becomes `docker.io/library/nginx`).
2. It filters them through the `images.exclude` globs.
3. It resolves each one to a digest.

Exclude patterns are path globs. `*` matches within one `/`-separated segment and `**` matches across segments. So
`ghcr.io/acme/**` drops everything under `ghcr.io/acme`, while `ghcr.io/acme/*` drops only its direct children (tag
included: `ghcr.io/acme/app:v1`). A chart that renders no images (CRDs only) needs `images.allowEmpty: true`.

**The lock is the source of truth for digests.** A tag resolves only when it is new to `images.lock.yaml`, for example
after a chart bump, a tracked-tag move or a new `images.extra` entry. A tag the lock already records keeps its locked
digest on every `upgrade` and `validate`, even if upstream has since rebuilt the tag in place. Mirror tags are
immutable, so a rebuilt digest could never be published under the same tag. If patchy followed the rebuild, `validate`
would report the committed tree as stale and `sync` could never publish it. patchy still checks that the locked digest
can be pulled upstream, warns when the tag has moved, and fails the entry if the digest is gone. A rebuild reaches the
mirror with the next upstream version. To adopt a rebuild on purpose, delete that image's entry from the lock and run
`upgrade`. The same rule applies to an artifact's `lock.yaml` while its `version` is unchanged.

Two features cover images that discovery cannot reach:

- **`images.distributionManifests`** handles operators that deploy workloads from manifests helm never renders. patchy
  pulls the named manifests artifact (`{appVersion}` expands), reads the Deployment images of the listed components, and
  writes them to the generated `images.extra.yaml` sidecar.
- **`images.track`** handles an image released on its own schedule (a runner image bumped more often than the chart).
  `upgrade` re-picks its tag within `versionConstraint` (by default the pinned tag's own major version), respecting the
  cooldown. It writes the pick into a values file at `valuesPath`. That value must already exist, and the pick replaces
  it.

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

`lint` rejects an `oci://` reference in the rendered output that points outside your registries. The platform would make
that pull at runtime and skip the review this store exists for. To allow such a reference, add an exception with a
reason:

```yaml
ociRefs:
  allow:
    - pattern: "oci://ghcr.io/acme/policies/*"
      reason: signed policy bundles verified by the admission controller
```

`pattern` is matched as a path glob against the full reference, including scheme and tag. `*` matches within one
`/`-separated segment and `**` matches across segments. So `oci://ghcr.io/acme/policies/*` admits
`oci://ghcr.io/acme/policies/base:1.2.0` but not `oci://ghcr.io/acme/policies/team/base:1.2.0`. A reference counts as
inside the mirror only when it sits under a configured registry's `url` at a path boundary, so a registry at
`registry.example.com/mirror` does not admit `oci://registry.example.com/mirror-old/chart`.

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
`allowlist` work the same way as a chart's.

## Lockstep groups

Charts released together upstream, such as an operator and its CRD chart, must not be mirrored at different versions.
Give them the same `chart.lockstep` (or `artifact.lockstep`) value. `upgrade` then targets the lowest of the members'
newest satisfying versions. The bump waits until upstream has published every member, so a half-mirrored release never
merges. `--group` selects a whole group: `patchy mirror upgrade --group flux --to 0.57.0`.

## The lifecycle

| Verb               | Touches          | What it does                                                                                                                                                                       |
| ------------------ | ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `upgrade`          | the working tree | resolves the newest version satisfying each constraint, re-picks tracked tags past their cooldown, re-vendors, re-renders, re-locks and (with `generate`) re-derives the allowlist |
| `upgrade --check`  | nothing          | the same plan, reported as data                                                                                                                                                    |
| `upgrade --pinned` | the working tree | regenerates from pins someone else moved (Renovate): picks nothing, but every pin must satisfy its constraint                                                                      |
| `validate`         | nothing          | regenerates out-of-tree and byte-compares, verifies provenance, scans, lints                                                                                                       |
| `sync`             | your registries  | publishes the committed state, idempotently                                                                                                                                        |

```sh
patchy mirror upgrade --check -o json        # what would move, as data
patchy mirror upgrade --all                  # move every pin, regenerate everything derived
patchy mirror validate --all                 # the gate
patchy mirror validate dex --only regen,lint # only the cheap stages
patchy mirror sync --all --dry-run           # what publishing would do
patchy mirror sync --all                     # converge every registry
```

`upgrade` is the only verb that reads the clock (tracked-tag cooldowns, allowlist expiry stamps). Because `validate`
never reads the clock, its byte comparison gives the same result for a commit and for the CI run that checks it.

`validate` runs four stages, selectable with `--only`:

- **regen**: vendor, render, discover and lock outside the working tree, then require the result to match the commit
  byte for byte.
- **verify**: check upstream provenance for the chart and every locked image.
- **scan**: scan the locked images and, with kubescape enabled, the rendered manifests. See
  [Scanning and allowlists](scanning.md).
- **lint**: check the rules that a schema-valid file can still break. Every source passes the
  [source policy](source-policy.md), every image has a provenance rule, and tracked images resolve. `oci://` references
  stay inside the mirror, and every allowlist entry has a statement and an expiry date within the allowed window.

`sync` re-pulls each upstream chart and fails loudly if upstream changed a released version (digest against the lock,
tree against the committed `vendor/`). Then, for each registry, it:

1. pushes the chart unless its tag already exists (an existing tag is never replaced)
2. copies every locked image by digest
3. signs everything it published that does not already carry a valid signature.

Before it pushes or signs anything for a subject, it verifies that subject's upstream provenance (see
[Provenance](#provenance-verifyupstream)). Skips count as successes, so a re-run after a partial failure finishes the
rest. `--workers` (default 4) limits how many registry operations run at once in each stage.

## Running a store in CI

The reference store, bitwise-media-group/flux-containers, runs four workflows. Each one is a few lines of shell around
patchy, and the markdown report is what people read. The steps below leave out checkout, tool installation and registry
login.

**Pull request: validate only what changed.** A config change affects every entry.

```sh
changed="$(git diff --name-only "origin/$BASE_REF"...HEAD -- charts/ artifacts/ | cut -d/ -f2 | sort -u | xargs)"
git diff --quiet "origin/$BASE_REF"...HEAD -- patchy.yaml || changed="--all"
patchy mirror validate $changed -o markdown > summary.md  # post as a sticky PR comment; the exit code is the gate
```

**Merge to main: publish.** `sync` checks upstream provenance itself before it pushes anything or signs it with the
platform identity. So a change that was merged without passing the PR gate still does not get the signature.

```sh
patchy mirror sync --all -o markdown >> "$GITHUB_STEP_SUMMARY"
```

The job needs `id-token: write` for keyless signing and a registry login with push rights. Pin the publish workflow to
the default branch: its certificate subject is what your clusters trust.

**Weekly: rescan what is already published.** Vulnerability databases keep changing after a merge. A rescan never blocks
anything. Findings become issues, which you fix by bumping the chart or by accepting them in the allowlist.

```sh
for dir in charts/*/ artifacts/*/; do
  entry="$(basename "$dir")"
  patchy mirror validate "$entry" --only scan,lint -o markdown > "rescan-$entry.md" || failed="$failed $entry"
done
```

**Daily: open update pull requests.** `upgrade --check -o json` reports the plan as lockstep groups, each with a target
and its members. Give each group one stable branch (`update/<group>`). Recreate it from main and update it in place, so
a newer upstream release rewrites the open PR instead of opening a second one:

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
post-upgrade task. It regenerates every derived file without picking any version itself. It fails if the new pin breaks
its constraint or splits a lockstep group.

## Migrating from `mirror.yaml`

Stores created before the layered [patchy configuration](configuration.md) kept their settings in a `mirror.yaml` at the
root. Every `mirror` command refuses to run while that file exists, and prints these steps instead:

1. Drop `apiVersion` and `kind`.
2. Keep `registries`, `signing` and `scan` at the top level of `patchy.yaml`.
3. Move `update` and `sourceRegistryRewrites` under `mirror:`.
4. Rename `scan.allowlistMaxDays` to `scan.allowlist.maxDays`, and `scan.allowlistNewDays` to `scan.allowlist.newDays`.
5. Delete `mirror.yaml`.

Compare `patchy mirror validate --all` before and after: the results should be identical.
