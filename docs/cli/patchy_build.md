## patchy build

Build, scan, publish and sign first-party images and Flux manifests

### Synopsis

Build the artifacts listed in the patchy config's build: block. These are
Dockerfile images (build.images) and Flux manifest directories (build.manifests).
The command scans them before anything is pushed, publishes them to every
configured registry as <registry.url>/<repository>:<tag>, and signs them with
that registry's signing block (or the global one). It is the counterpart of
`patchy mirror` for code you write, and it shares the registries, signing and
scan blocks.

Images build with `docker buildx` into an OCI layout. Every platform image is
scanned (osv-scanner, grype, or both) under the entry's resolved scan policy and
its allowlist, security/allowlists/<name>.yaml. Any blocking finding the
allowlist does not accept fails the entry, and nothing is pushed. A lapsed
allowlist entry is reported and accepts nothing. Manifests directories are
staged (ignore patterns applied, symlinks refused), scanned by kubescape, and
published as-is as a Flux OCI artifact.

Tags come from ${ENV} templates: ${VAR}, ${VAR:-default}, ${VAR:offset:length}
and $$, plus the built-in ${PATCHY_TIMESTAMP}. That is UTC yyyymmddhhmmss, fixed
once per run. It comes from SOURCE_DATE_EPOCH when that is set, and a real
PATCHY_TIMESTAMP variable overrides it. Tags never move. A missing tag is pushed,
a tag already at the build's digest is skipped, and a tag at another digest is
left alone with a warning. With SOURCE_DATE_EPOCH set, rebuilding one commit
gives the same digest.

--dry-run builds and scans but never contacts a registry, so it is safe in PR CI
with no push credentials. It needs no registries configured: without any, it
reports each build's digest. --update-allowlists rebuilds and rescans each image
entry whose scan.allowlist.generate is true, and regenerates its allowlist. It
never pushes, signs, or fails on findings. Reviewers accept the risk when they
review the allowlist diff in the PR.

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
      --all[=true|false]                 build every configured entry, continuing past failures (none configured is a no-op)
      --dry-run[=true|false]             build and scan, but never contact a registry
  -h, --help[=true|false]                help for build
      --registry strings                 restrict publishing to the named registries (repeatable or comma-separated; default all)
      --tag strings                      replace the configured tag templates for this run (repeatable; still ${ENV}-expanded)
      --update-allowlists[=true|false]   rebuild, rescan and regenerate security/allowlists/<name>.yaml; never publishes
```

### Options inherited from parent commands

```
  -A, --all-namespaces[=true|false]   work across every namespace
      --context string                kubeconfig context to use
      --kubeconfig string             path to the kubeconfig file
  -n, --namespace string              namespace to work in (default: the context's)
      --no-color[=true|false]         disable colour and styling
  -o, --output string                 output format: table, wide, json, yaml, name, or markdown (default "table")
      --request-timeout duration      timeout for a single API call (default 30s)
  -v, --verbose[=true|false]          log what the CLI is doing to stderr
```

### SEE ALSO

* [patchy](patchy.md)	 - Work with patchy security findings from the terminal
* [patchy build promote](patchy_build_promote.md)	 - Promote published builds by adding a tag to them, without rebuilding

