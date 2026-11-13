## patchy mirror sync

Publish committed charts and images to the mirror registries

### Synopsis

Converge every configured registry onto the committed state, idempotently:
for each entry, re-pull the upstream chart archive and fail loudly if upstream
mutated the released version (digest vs lock, tree vs the committed vendor/),
then per registry push the chart unless its tag already exists (an existing
tag is never replaced), copy every locked image by digest, and sign everything
published that does not already carry a valid mirror signature — using that
registry's signing block when it has one, else the global default.

Before a chart, image or artifact is first pushed or signed, its upstream
provenance is verified per its verifyUpstream rule — once per run however many
registries it publishes to, and never for a copy already current and signed
everywhere — so nothing reaches a registry unverified even when validate
never ran. --dry-run reports what it would verify without calling cosign.

--registry restricts publishing to the named registries (repeatable or
comma-separated); the default is all of them.

sync is read-only on the working tree — every write goes to a registry —
so the publish-on-merge pipeline creates no commits. Skips are successes;
a re-run after a partial failure finishes the remainder.

```
patchy mirror sync [name]... [flags]
```

### Examples

```
  patchy mirror sync --all
  patchy mirror sync --all -o markdown       # publish summary, ready to paste
  patchy mirror sync --all --dry-run -o json
  patchy mirror sync --all --registry ghcr   # one registry only
  patchy mirror sync opentelemetry-collector
```

### Options

```
      --all[=true|false]       sync every entry in the store
      --dry-run[=true|false]   report every action without writing to the registry
  -h, --help[=true|false]      help for sync
      --registry strings       restrict publishing to the named registries (repeatable or comma-separated; default all)
```

### Options inherited from parent commands

```
  -A, --all-namespaces[=true|false]   work across every namespace
      --context string                kubeconfig context to use
  -C, --directory string              mirror store directory, relative to the working directory (default: the working directory)
      --kubeconfig string             path to the kubeconfig file
  -n, --namespace string              namespace to work in (default: the context's)
      --no-color[=true|false]         disable colour and styling
  -o, --output string                 output format: table, wide, json, yaml, name, or markdown (default "table")
      --request-timeout duration      timeout for a single API call (default 30s)
  -v, --verbose[=true|false]          log what the CLI is doing to stderr
      --workers int                   concurrent registry operations per stage (default 4)
```

### SEE ALSO

* [patchy mirror](patchy_mirror.md)	 - Mirror upstream helm charts and OCI artifacts into a platform registry

