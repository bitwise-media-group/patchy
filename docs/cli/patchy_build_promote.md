## patchy build promote

Promote published builds by adding a tag to them, without rebuilding

### Synopsis

Promote an already-published build of each entry by adding a tag to its digest:
what was tested is exactly what ships. In every configured registry, the newest
tag of <registry.url>/<repository> matching --from (a glob; semver order, so
X.Y.Z-edge.<yyyymmddhhmmss>.g<commit> builds order by their timestamp) is the
build promoted. Its digest must carry a valid signature under that registry's
signing config, and --tag is then added to it.

Tags never move: a --tag already on that digest is skipped, and one on another
digest fails the entry. Nothing is built, scanned or signed — the build's own
signature is the one verified. --from and --tag are ${ENV}-expanded like the
build's tag templates.

Each row reports the build's org.opencontainers.image.revision annotation (every
manifests artifact carries one), so a release pipeline can check the build came
from the commit it is releasing. --dry-run resolves and verifies, and tags
nothing.

The patchy config merges from, least to most preferred: /etc/patchy/config.<ext>,
~/.config/patchy/config.<ext>, then in the working directory mise/patchy.<ext>,
patchy/config.<ext>, .mise/patchy.<ext>, .patchy/config.<ext> and patchy.<ext>
(<ext> is yaml, yml, json or toml). Maps merge, lists replace, and null deletes
an inherited key.

```
patchy build promote [name]... --from <pattern> --tag <tag> [flags]
```

### Examples

```
  patchy build promote --all --from '${VERSION}-edge.*' --tag 'v${VERSION}'
  patchy build promote platform --from '1.4.0-edge.*' --tag v1.4.0 --dry-run -o json
```

### Options

```
      --all[=true|false]       promote every configured entry, continuing past failures (none configured is a no-op)
      --dry-run[=true|false]   resolve and verify the builds to promote, but tag nothing
      --from string            glob over the repository's tags selecting the builds to promote from; the newest wins (${ENV}-expanded)
  -h, --help[=true|false]      help for promote
      --registry strings       restrict promotion to the named registries (repeatable or comma-separated; default all)
      --tag string             the tag to add to the promoted build (${ENV}-expanded)
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

* [patchy build](patchy_build.md)	 - Build, scan, publish and sign first-party images and Flux manifests

