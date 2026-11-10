## patchy build

Build, scan, publish and sign first-party images and Flux manifests

### Synopsis

Build the artifacts the patchy config's build: block lists — Dockerfile images
(build.images) and Flux manifest directories (build.manifests) — scan them
before anything is pushed, publish them to every configured registry as
<registry.url>/<repository>:<tag>, and sign them with that registry's signing
block (else the global one). It is the first-party sibling of `patchy mirror`
and shares its registries, signing and scan blocks.

Images build with `docker buildx` into an OCI layout; every platform image is
scanned (osv-scanner and/or grype) under the entry's resolved scan policy and
its allowlist, security/allowlists/<name>.yaml. Any blocking finding the
allowlist does not accept fails the entry with nothing pushed; a lapsed
allowlist entry is reported and accepts nothing. Manifests directories are
staged (ignore patterns applied, symlinks refused), swept by kubescape, and
published as-is as a Flux OCI artifact.

Tags come from ${ENV} templates: ${VAR}, ${VAR:-default}, ${VAR:offset:length}
and $$, plus the built-in ${PATCHY_TIMESTAMP} (UTC yyyymmddhhmmss, fixed once per
run; from SOURCE_DATE_EPOCH when set, overridable by a real PATCHY_TIMESTAMP).
Tags never move: an absent tag is pushed, one already at the build's digest is
skipped, and one at another digest is left alone with a warning. With
SOURCE_DATE_EPOCH set, rebuilds of one commit reproduce the same digest.

--dry-run builds and scans but never contacts a registry (safe in PR CI with
no push credentials). --update-allowlists rebuilds and rescans each image
entry whose scan.allowlist.generate is true and regenerates its allowlist —
never pushing, signing, or failing on findings: the PR review of the diff is
where the risk is accepted.

Needs docker with buildx (multi-platform builds need a docker-container
builder or the containerd image store), cosign (>= v3) for signing, and the
enabled scanners on PATH.

The patchy config merges from, least to most preferred: /etc/patchy/config.<ext>,
~/.config/patchy/config.<ext>, then in the working directory mise/patchy.<ext>,
patchy/config.<ext>, .mise/patchy.<ext>, .patchy/config.<ext> and patchy.<ext>
(<ext> is yaml, yml, json or toml). Maps merge, lists replace, and null deletes
an inherited key.

```
patchy build [name]... [flags]
```

### Examples

```
  patchy build --all --dry-run                      # PR CI: build and scan only
  patchy build --all -o markdown                    # publish, summary ready to paste
  patchy build controller --registry ghcr
  patchy build --all --tag 'v${VERSION}'            # replace the configured tags
  patchy build --all --update-allowlists            # regenerate security/allowlists/
```

### Options

```
      --all                 build every configured entry, continuing past failures (none configured is a no-op)
      --dry-run             build and scan, but never contact a registry
  -h, --help                help for build
      --registry strings    restrict publishing to the named registries (repeatable or comma-separated; default all)
      --tag strings         replace the configured tag templates for this run (repeatable; still ${ENV}-expanded)
      --update-allowlists   rebuild, rescan and regenerate security/allowlists/<name>.yaml; never publishes
```

### Options inherited from parent commands

```
  -A, --all-namespaces             work across every namespace
      --context string             kubeconfig context to use
      --kubeconfig string          path to the kubeconfig file
  -n, --namespace string           namespace to work in (default: the context's)
      --no-color                   disable colour and styling
  -o, --output string              output format: table, wide, json, yaml, name, or markdown (default "table")
      --request-timeout duration   timeout for a single API call (default 30s)
  -v, --verbose                    log what the CLI is doing to stderr
```

### SEE ALSO

* [patchy](patchy.md)	 - Work with patchy security findings from the terminal

