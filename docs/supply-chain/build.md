# Building images and Flux manifests

`patchy build` is the first-party counterpart of [`patchy mirror`](mirror.md). It builds the Dockerfiles and Flux
manifest directories listed in the config's `build:` block, scans them **before** anything is pushed, publishes them to
every configured registry as `<registry.url>/<repository>:<tag>`, and signs them. It uses the same
[registries and signing](index.md#registries), the same scanners and the same [allowlist rules](scanning.md) as the
mirror.

For every flag, see the [command reference](../cli/patchy_build.md).

## The `build:` block

```yaml
registries:
  - name: ghcr
    url: ghcr.io/acme

build:
  architectures: [amd64, arm64] # default for image entries; amd64 means linux/amd64
  tags: # default for every entry; an entry's own list replaces it
    - "${VERSION}-${CHANNEL:-edge}.${PATCHY_TIMESTAMP}.g${GITHUB_SHA:0:7}"
    - "sha-${GITHUB_SHA}"
  images:
    - name: controller # identity: CLI selection, results, the allowlist file
      repository: platform/controller # published as ghcr.io/acme/platform/controller:<tag>
      dockerfile: Dockerfile.controller # default <context>/Dockerfile
      context: .
      target: runtime # optional build stage
      args: { GO_VERSION: "1.27" } # values ${ENV}-expanded, key case preserved
      scan:
        failOn: [CRITICAL]
        allowlist: { generate: true, maxDays: 30 }
  manifests:
    - name: platform
      repository: manifests/platform
      path: deploy/flux
      ignore: ["*.md", "tests/"] # gitignore syntax; .git/ is always ignored
      source: "${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}"
      revision: "${GITHUB_REF_NAME}@sha1:${GITHUB_SHA}"
```

Entry names must be unique across images and manifests, because they name allowlist files and CLI arguments. Relative
paths (`dockerfile`, `context`, `path`) resolve against the working directory, whichever config layer holds them.
`repository` follows the OCI path grammar.

```sh
patchy build --all --dry-run              # build and scan, never contact a registry
patchy build --all                        # build, scan, publish, sign
patchy build controller --registry ghcr   # one entry, one registry
patchy build --all --tag '${VERSION}'     # replace the configured tag templates for this run
patchy build --all --update-allowlists    # regenerate security/allowlists/<name>.yaml
```

Entries run one at a time (buildx parallelizes inside a build). `--all` carries on past a failed entry and exits
non-zero at the end. An empty `build:` block makes `--all` a no-op that reports nothing to build, so one shared task can
run it in every repository.

## Images

Each image entry runs:

```text
docker buildx build --file <dockerfile> --platform <platforms> [--target <t>] [--build-arg K=V]... \
  --output type=oci,dest=<tmp>,tar=false[,rewrite-timestamp=true] <context>
```

The result is a local OCI layout. Each platform image in it is exported as a docker archive and scanned under the
entry's [scan policy](scanning.md) (attestation manifests are skipped). Blocking findings are checked against
`security/allowlists/<name>.yaml`. Anything still blocking fails the entry, and **nothing is pushed**. The allowlist is
also linted before the build starts, so one that breaks the rules (missing statement, expiry beyond `maxDays`) fails
fast. An entry whose acceptance has lapsed is reported and accepts nothing.

**Platforms.** `architectures` takes `amd64`, `arm64`, … (meaning `linux/<arch>`) or full `os/arch[/variant]` values. A
multi-platform OCI export needs a `docker-container` builder or the containerd image store; the default `docker` driver
refuses it:

```sh
docker buildx create --name patchy --driver docker-container --use
```

Building for a foreign architecture also needs QEMU emulation registered
(`docker run --privileged --rm tonistiigi/binfmt --install all`, or `docker/setup-qemu-action` in GitHub Actions).

**Reproducible digests.** With `SOURCE_DATE_EPOCH` set, patchy asks buildx to rewrite layer timestamps, and
`${PATCHY_TIMESTAMP}` is taken from it too. Set it to the commit time and a rebuild of the same commit produces the same
tags, and the same digest as long as the Dockerfile itself is deterministic (pinned base images by digest, no
unversioned package installs):

```sh
export SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)"
```

A re-run of a reproducible build reports `skipped-current` on every target.

## Flux manifests

A manifests entry publishes a directory as-is, as a Flux OCI artifact, with no render step. The tree under `path` is
staged with the `ignore` patterns applied (gitignore syntax; `.git/` always excluded). Only regular files and
directories are allowed, and a symlink fails the entry rather than being followed. If scanning is enabled, kubescape
sweeps the staged tree, so it sees exactly what gets published. The tree is then packaged:

| Part        | Value                                                                                                |
| ----------- | ---------------------------------------------------------------------------------------------------- |
| config      | `application/vnd.cncf.flux.config.v1+json`, contents `{}`                                            |
| layer       | one `application/vnd.cncf.flux.content.v1.tar+gzip`                                                  |
| annotations | `org.opencontainers.image.source` from `source`, `org.opencontainers.image.revision` from `revision` |

The tarball is deterministic: entries are sorted, timestamps zeroed, owners zeroed, modes normalized to `0644` / `0755`,
and the gzip header carries no name or time. An unchanged tree with the same annotations always produces the same
digest. There is deliberately no `created` annotation, which would break that.

`flux pull artifact`, `flux build`, and an `OCIRepository` all consume it directly:

```sh
flux pull artifact oci://ghcr.io/acme/manifests/platform:1.4.0 --output ./platform
```

Manifests entries only get the configuration scan. `failOn`, `ignoreUnfixed` and `allowlist` are image-scan settings, so
setting them on a manifests entry is a validation error. Inherited config defaults are simply not used.

## Tags

Tag templates, image `args`, `source` and `revision` all expand environment references:

| Syntax                 | Meaning                                                             |
| ---------------------- | ------------------------------------------------------------------- |
| `${VAR}`               | the value of `VAR`; unset is an error naming the entry and template |
| `${VAR:-default}`      | the value, or `default` when unset or empty                         |
| `${VAR:offset:length}` | a substring: `${GITHUB_SHA:0:7}`                                    |
| `$$`                   | a literal `$`; any other bare `$` is an error                       |
| `${PATCHY_TIMESTAMP}`  | built in: UTC `yyyymmddhhmmss`, fixed once per run                  |

`PATCHY_TIMESTAMP` comes from `SOURCE_DATE_EPOCH` when that is set, and a real `PATCHY_TIMESTAMP` environment variable
overrides both, so several jobs can share one value. Expansion happens before anything is built, so a missing variable
fails in seconds rather than after a long build. Every expanded tag must be a valid OCI tag.

### Channels without moving tags

**Tags never move.** For every registry and tag:

| The tag…                              | Action                                                 |
| ------------------------------------- | ------------------------------------------------------ |
| does not exist                        | `pushed`                                               |
| already points at this build's digest | `skipped-current`                                      |
| points at a different digest          | `skipped-tag-exists`, with a warning; it is left alone |
| (with `--dry-run`)                    | `would-push`                                           |

That makes `build` safe against registries with immutable tags, and it means a channel can't be a moving tag. A channel
is instead its **newest immutable build**, written as a semver pre-release that sorts by time:

| Channel | Tag                                     | Consumer follows                                                                                |
| ------- | --------------------------------------- | ----------------------------------------------------------------------------------------------- |
| edge    | `1.4.0-edge.20261110090807.g0123456`    | semver `>=0.0.0-0`, filtered to `^[0-9]+[.][0-9]+[.][0-9]+-edge[.][0-9]{14}[.]g[0-9a-f]{7,40}$` |
| staging | `1.4.0-staging.20261110090807.g0123456` | semver `>=0.0.0-0`, filtered to the same pattern with `staging`                                 |
| stable  | `1.4.0`                                 | semver `>=0.0.0`, which excludes pre-releases                                                   |

A Flux `OCIRepository` follows a channel with `ref.semver` plus `ref.semverFilter`. The templates that produce the
pre-release tags are the default `tags` above, with `CHANNEL` set per workflow. `sha-${GITHUB_SHA}` gives every build a
second, commit-addressed name.

**Promotion** adds the plain version tag to a build that already exists. A manifests artifact is a pure function of its
tree and its `source` and `revision` values, so rebuilding the released commit with the same values reproduces the
staging build's digest, and `--tag` lands the new tag on it:

```sh
git checkout v1.4.0
VERSION=1.4.0 patchy build --all --tag '${VERSION}'
```

For this to work, keep anything that differs between the two runs out of `revision`. In particular, don't use the
workflow's ref name, which differs between the release run and a manual dispatch. Image entries reproduce the same way
only if their build is reproducible (see above). Otherwise the stable tag lands on a new digest built from the same
commit.

## Publishing and signing

After the tags are written, every registry where at least one tag holds the build's digest gets it signed with that
registry's signing block, or the global one. An image index is signed recursively, so every platform manifest is too.
Before signing, patchy checks for an existing valid signature with the configured identity, so re-runs don't stack up
signatures. See [Signing](index.md#signing).

The report has one row per (entry, registry, tag): `ENTRY`, `REGISTRY`, `REF`, `ACTION`, `SIGNED`. `-o json` adds the
digest and the scan summary. `-o markdown` gives one section per entry with its digest, a scan line, a bullet per
target, and a fenced block listing any blocking findings, ready for a job summary.

## Dry runs

`--dry-run` builds and scans everything and reports `would-push` for every target, but never contacts a registry. That
makes it the pull-request gate. It needs no push credentials and no signing identity, but it does need the variables
your tag templates reference, because they are expanded first.

## Allowlists

Each image entry's allowlist lives at `security/allowlists/<name>.yaml` (relative to the working directory), in the same
format as a mirror entry's. `--update-allowlists` is build's version of the mirror's upgrade-time derivation. For each
selected image entry whose resolved `scan.allowlist.generate` is true, it rebuilds, rescans and regenerates the file:
surviving acceptances keep their expiry, new ones get `newDays`, and findings that have gone are dropped. It never
pushes, never signs and never fails on findings. Reviewing the allowlist diff in a pull request is where the risk is
accepted. Entries with `generate` off are skipped with a note. See [Scanning and allowlists](scanning.md#allowlists).

## Running it in CI

GitHub Actions examples, with checkout, tool installation and registry login trimmed. Log in to every registry host
before publishing (`docker login`, or a cloud auth action). Keyless signing needs `permissions: id-token: write`.

**Pull request: build and scan.**

```yaml
- uses: docker/setup-qemu-action@<sha> # only for foreign-architecture images
- uses: docker/setup-buildx-action@<sha> # a docker-container builder
- run: patchy build --all --dry-run -o markdown >> "$GITHUB_STEP_SUMMARY"
  env:
    VERSION: "0.0.0" # anything that expands; nothing is published
```

**Push to main: an edge build.** release-please repositories already record the last release in
`.release-please-manifest.json`, which makes a monotonic base for edge builds.

```sh
export VERSION="$(jq -r '."."' .release-please-manifest.json)" CHANNEL=edge
export SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)"
patchy build --all -o markdown >> "$GITHUB_STEP_SUMMARY"
```

**Release: a staging build.**

```sh
export VERSION="${TAG#v}" CHANNEL=staging SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)"
patchy build --all -o markdown >> "$GITHUB_STEP_SUMMARY"
```

**Scheduled: refresh allowlists.** Run `patchy build --all --update-allowlists` and open a pull request with whatever
changed under `security/allowlists/`, the same way the mirror's update sweep does.

## Troubleshooting

| Symptom                                               | Cause and fix                                                                                                                                          |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `OCI exporter is not supported for the docker driver` | the active builder is the plain `docker` driver: create a `docker-container` builder (above) or turn on the containerd image store                     |
| `exec format error` during a build step               | a foreign-architecture `RUN` without QEMU: register binfmt handlers                                                                                    |
| `${VERSION} is not set`                               | a tag, arg, `source` or `revision` template references an unset variable: set it, or give it a `${VERSION:-default}`                                   |
| `no image scanner enabled`                            | image scanning is on but `scan.scanners.osv` and `scan.scanners.grype` are both off: enable one, or set the entry's `scan.enabled: false` deliberately |
| `skipped-tag-exists`                                  | the tag already names another digest: tags never move, so build a new tag (the timestamp and commit in the default templates make each build unique)   |
| allowlist lint errors before the build                | `security/allowlists/<name>.yaml` breaks policy: every entry needs `id`, `statement` and an `expired_at` within `maxDays`                              |
