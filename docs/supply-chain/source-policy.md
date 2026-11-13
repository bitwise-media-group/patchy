# Restricting sources

A **source policy** limits where `patchy mirror` and `patchy build` may pull from. The mirror gets one policy for
upstream charts, images and artifacts. The build gets another for the base images its Dockerfiles use. The two are
independent: the registries a mirror copies from are usually not the ones a build should take base images from.

```yaml
mirror:
  policy:
    mode: fail # fail (default) | warn
    enforced: true # see "Enforcing a policy" below
    charts: # "<chart.repo>/<chart.name>", scheme kept
      allow:
        - oci://ghcr.io/fluxcd-community/charts/*
        - https://charts.jetstack.io/*
    images: # the canonical repository: no tag, no digest
      allow:
        - registry.k8s.io/**
        - ghcr.io/fluxcd/*
        - docker.io/library/*
      deny:
        - docker.io/library/busybox
    artifacts: # artifact.ref and images.distributionManifests.artifact
      allow:
        - ghcr.io/fluxcd/**

build:
  policy:
    mode: fail
    images: # every image a Dockerfile pulls
      allow:
        - registry.example.com/images/**
```

## Matching

Each kind (`charts`, `images`, `artifacts`) has its own `allow` and `deny` lists:

- A `deny` match always rejects.
- When `allow` is non-empty, a source must match one of its patterns.
- A kind with neither list is unrestricted, and so is a config with no `policy` block.

Patterns are path globs:

| Pattern           | Matches                                                                                            |
| ----------------- | -------------------------------------------------------------------------------------------------- |
| `ghcr.io/acme/*`  | `ghcr.io/acme/app`, but not `ghcr.io/acme/team/app`: `*` stays within one `/`-separated segment    |
| `ghcr.io/acme/**` | anything under `ghcr.io/acme/`, at any depth                                                       |
| `ghcr.io/**/app`  | `ghcr.io/app`, `ghcr.io/a/b/app`: a `**/` that starts a segment also matches zero segments         |
| `ghcr.io/acme*`   | `ghcr.io/acme-tools`. The prefix is still literal, so write `ghcr.io/acme/**` to mean "under acme" |
| `**`              | everything                                                                                         |

`?` matches one character other than `/`. Nothing else is special.

Images and artifacts are matched in their canonical form: shorthand expands the way container runtimes expand it, and
tags and digests are dropped. So `nginx:1.27` is matched as `docker.io/library/nginx`, and a pattern must be written the
same way. Patterns are checked when the config loads, and three that could never match are rejected: one with a tag or
digest, one with no registry host (`library/nginx`), and a lone `*`, which matches a single segment and so nothing. Use
`**` for "everything".

Charts are matched as `<chart.repo>/<chart.name>` with the scheme kept, so the transport is part of the decision:
`oci://ghcr.io/acme/charts/app`, `https://charts.example.com/app`.

The mirror matches the **canonical** source, never the host a `sourceRegistryRewrites` entry reroutes pulls through. A
pull-through cache does not change what a source is.

## Modes

`fail` (the default) blocks:

- **mirror**: `upgrade` and `validate`'s regen stage refuse before pulling the chart, the distribution artifact, or any
  image. `lint` reports each rejected source as an issue. `sync` refuses before any pull or push, even for a lock
  committed before the policy existed.
- **build**: the build fails before buildx runs, naming the Dockerfile line.

`warn` reports each rejected source as a warning, once per entry and source, and lets the run continue. Use it to see
what a new policy would reject before switching to `fail`.

## What a build policy checks

The Dockerfile is parsed with BuildKit's own parser and expanded the way BuildKit expands it. Every stage counts,
whether or not the target depends on it. These all pull an image, so all are checked:

- each `FROM`, other than an earlier stage or `scratch`
- `COPY --from=<image>`
- `RUN --mount=…,from=<image>`
- the frontend: the `# syntax=` directive, or the `BUILDKIT_SYNTAX` build arg. With a strict allow list, mirror the
  frontend too, or allow `docker.io/docker/dockerfile`.

References expand from the global `ARG` defaults, overridden by the entry's `args`, plus BuildKit's automatic platform
args, once per target platform. A stage's own `ARG` and `ENV` values also apply to its `--from` flags. A reference that
cannot be resolved is rejected under the strictest policy restricting images, because it cannot be shown to be
permitted. An example is `FROM ${REGISTRY}/app` with `REGISTRY` unset.

A build policy governs image pulls only. `ADD <url>` and network access from `RUN` are outside its scope.

## Enforcing a policy

The patchy config is [layered](../cli.md#configuration): normally a more preferred layer replaces a list wholesale and
deletes a key with `null`. A policy shipped from a shared location, such as the toolchain's `.mise/patchy.yaml`, would
then be one line away from being switched off by a project's own `patchy.yaml`.

`enforced: true` pins a policy block to the layer that declares it. patchy evaluates that block **as written in its own
file**, alongside the merged result, and a source must pass both. A more preferred layer can therefore still tighten the
policy, for example by adding a `deny` entry or narrowing `allow`. Changing its `mode`, replacing its lists, setting
`enforced: false` or writing `policy: null` all change only the merged block, never the pinned one.

A block without `enforced` merges like any other key, so a project can override it. Leave `enforced` off where that
flexibility is intended.

A violation names the layer that rejected it:

```text
source policy: image gcr.io/distroless/static is not permitted: matches no allow pattern
  (images.allow/deny in .mise/patchy.yaml, enforced)
```

`enforced` is a guardrail on the client side: whoever controls the files controls the policy. The firm enforcement point
is CI running `patchy mirror validate` or `patchy build --dry-run` against a toolchain it pins.
