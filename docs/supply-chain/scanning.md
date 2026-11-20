# Scanning and allowlists

`patchy mirror` and `patchy build` apply one scan policy to everything they publish. A mirrored chart's images, a
mirrored artifact and a first-party image all go through the same scanners, the same blocking rules and the same
allowlist format. Nothing is published while it has a blocking finding that no one has accepted.

## Scanners

Every scanner is an external binary, enabled in the config's `scan.scanners` block:

```yaml
scan:
  scanners:
    osv: { enabled: true } # osv-scanner v2
    grype: { enabled: false } # grype; either or both
    kubescape: { enabled: true, mode: warn } # configuration scan; warn | fail
```

| Scanner   | Scans                    | Invocation                                                                                   |
| --------- | ------------------------ | -------------------------------------------------------------------------------------------- |
| osv       | container images         | `osv-scanner scan image --archive` over a docker archive                                     |
| grype     | container images         | `grype registry:<ref>` for a mirrored image, `grype docker-archive:<path>` for a local build |
| kubescape | Kubernetes configuration | `kubescape scan <path>` with an empty `KUBECONFIG`, so no cluster is ever consulted          |

Image scanners are off by default. An image scan with neither osv nor grype enabled is an error. So to turn image
scanning off, you must set `scan.enabled: false` explicitly. When both scanners are on, both run and their findings are
combined.

kubescape only reports by default. In `warn` mode its findings are printed and never block. Configuration findings such
as broad RBAC or missing security contexts are often unavoidable for what an operator chart has to do. In `fail` mode,
any finding fails the entry. When `kubescape` is not on `PATH`, the scan is skipped with a warning.

| What              | Image scan                              | Configuration scan                       |
| ----------------- | --------------------------------------- | ---------------------------------------- |
| mirrored chart    | every locked image                      | the rendered manifests                   |
| mirrored artifact | the artifact, if it is a runnable image | none                                     |
| build image       | every platform image in the build       | none                                     |
| build manifests   | none                                    | the staged tree, as it will be published |

## The `scan` block

The `scan` block has the same shape everywhere. In the config it holds the defaults. In a chart manifest, an artifact
manifest or a build entry it overrides them field by field. Each field takes the entry's value when the entry sets it.
Otherwise it takes the config's value, and failing that, the built-in default.

```yaml
# config (defaults)                     # chart / artifact manifest, build entry (overrides)
scan:                                   scan:
  enabled: true      # master default     enabled: auto      # auto | true | false
  scanners: {...}    # config only        failOn: [CRITICAL]
  failOn: [CRITICAL, HIGH]                ignoreUnfixed: false
  ignoreUnfixed: true                     allowlist:
  allowlist:                                generate: true
    maxDays: 90                             maxDays: 30
    newDays: 90
    generate: false
    preamble: ""
```

| Field                | Built-in default   | Meaning                                                               |
| -------------------- | ------------------ | --------------------------------------------------------------------- |
| `failOn`             | `[CRITICAL, HIGH]` | severities that block                                                 |
| `ignoreUnfixed`      | `true`             | findings with no released fix never block                             |
| `allowlist.maxDays`  | `90`               | allowlist entries must expire within this many days                   |
| `allowlist.newDays`  | `90`               | expiry stamped on newly derived allowlist entries (at most `maxDays`) |
| `allowlist.generate` | `false`            | regenerate the entry's allowlist from a fresh scan                    |
| `allowlist.preamble` | none               | whole-file context rendered at the top of a generated allowlist       |

An entry's `enabled` defaults to `auto`. `auto` follows the config's `scan.enabled`, and then the rule for the entry's
kind in the table below. An explicit `true` or `false` on the entry overrides `scan.enabled` for that entry. `false`
turns off every scan, kubescape included.

| Kind            | `auto` means                                                                        |
| --------------- | ----------------------------------------------------------------------------------- |
| Chart           | scan its locked images, and run kubescape over the rendered manifests               |
| Artifact        | scan only if it is a runnable image                                                 |
| Build image     | scan every platform image                                                           |
| Build manifests | kubescape only. `failOn`, `ignoreUnfixed` and `allowlist` are rejected on the entry |

## What blocks

patchy maps every scanner's severities onto one scale: `CRITICAL`, `HIGH`, `MEDIUM`, `LOW`, `NEGLIGIBLE`, then
`UNKNOWN`. A finding blocks when:

1. its severity is in the resolved `failOn`,
2. it has a released fix, or `ignoreUnfixed` is off, and
3. no live allowlist entry accepts it, by its ID or any of its aliases (a `CVE-…` entry also accepts the matching
   `GHSA-…` or `GO-…` report).

Blocking findings fail `patchy mirror validate` and fail a `patchy build` entry before anything is pushed. `-o markdown`
lists them in a fenced block per entry, ready for a PR comment.

## Allowlists

An allowlist is a reviewed record of accepted risk. Each mirror or build entry has its own file:

| Entry                      | File                                                                                |
| -------------------------- | ----------------------------------------------------------------------------------- |
| mirrored chart or artifact | `charts/<name>/security/allowlist.yaml`, `artifacts/<name>/security/allowlist.yaml` |
| build image                | `security/allowlists/<name>.yaml`, relative to the working directory                |

```yaml
vulnerabilities:
  - id: CVE-2026-1234 # the finding's ID, or any alias of it
    statement: Fixed in libfoo 1.2.4; the locked image ships 1.2.3. Accepted in PR review.
    expired_at: "2027-01-20" # YYYY-MM-DD, within allowlist.maxDays of today
    notes: | # optional human analysis; preserved across regeneration
      Only reachable through the admin API, which is not exposed.
```

`mirror validate --only lint` and every `build` enforce these rules:

- every entry has an `id`, a `statement` and a parseable `expired_at`;
- `expired_at` is no more than `allowlist.maxDays` in the future, so an acceptance can't last forever;
- an entry whose `expired_at` is today or earlier has **lapsed**. A lapsed entry fails mirror lint, and for build it is
  reported and accepts nothing. Either way, the finding needs reviewing again.

### Generated allowlists

Once images update on their own schedule, keeping a per-CVE list by hand turns into copying rather than reviewing. With
`scan.allowlist.generate: true`, patchy does the copying, and people accept the risk by reviewing the pull request.
`patchy mirror upgrade` regenerates a mirror entry's file, and `patchy build --update-allowlists` regenerates a build
entry's. `validate` and a plain `build` never regenerate it. The set of findings changes with the vulnerability
databases, and those verbs must stay deterministic.

Three rules keep a regenerated file trustworthy:

- **Gone findings are dropped.** A stale acceptance looks the same as a live one, so no entry outlives its finding.
- **Surviving entries keep their expiry.** Regeneration never rolls the clock forward. New entries get today plus
  `newDays`, and a surviving entry that has already lapsed is renewed like a new acceptance, so the date change shows up
  in the diff for review.
- **Notes are preserved.** patchy writes the `statement` from scan data. It records which packages are affected, the
  smallest released fix, the version the image ships, and where the package was found. That location is the binary a Go
  module is compiled into, or the lockfile or package database that declares it. On a mirrored entry the paths are
  grouped by image reference (`Found in ghcr.io/x/controller@sha256:…: /manager.`). A build entry is one image, so it
  lists the paths alone (`Found in /usr/local/bin/controller.`). Put your own analysis of whether the code is reachable
  in `notes`, which regeneration keeps word for word.

The file starts with a generated banner, followed by the entry's `allowlist.preamble`. Use the preamble for context that
covers the whole file, such as "images ship in lockstep with the chart; the next chart release clears these":

```yaml
scan:
  allowlist:
    generate: true
    preamble: |
      cert-manager images are released in lockstep with the chart, so an entry here means no fixed release exists
      yet; the chart upgrade is what clears it.
```

## Rescanning what is already published

A clean scan at merge time says nothing about next month's CVEs. Rescan what you have already published on a schedule
with `patchy mirror validate <entry> --only scan,lint -o markdown`. Turn failures into issues instead of blocking
anything. The [mirror guide](mirror.md#running-a-store-in-ci) has the loop. The lint stage in the same run catches
allowlist entries that are about to lapse.
