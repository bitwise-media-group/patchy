## patchy mirror sync

Publish committed charts and images to the mirror registries

### Synopsis

Bring every configured registry in line with the committed state. The command
is idempotent. For each entry it re-pulls the upstream chart archive, and fails
loudly if upstream changed the released version (digest against the lock, tree
against the committed vendor/). Then, for each registry, it pushes the chart
unless its tag already exists (an existing tag is never replaced), copies every
locked image by digest, and signs everything it published that does not already
carry a valid mirror signature. It signs with that registry's signing block when
it has one, or else the global default.

Before a chart, image or artifact is first pushed or signed, sync checks its
upstream provenance against its verifyUpstream rule. Each one is checked once
per run however many registries it publishes to, and never when its copy is
already current and signed everywhere. So nothing reaches a registry unverified,
even when validate never ran. --dry-run reports what it would verify without
calling cosign.

--registry restricts publishing to the named registries (repeatable or
comma-separated). The default is all of them.

sync never writes to the working tree. Every write goes to a registry, so the
publish-on-merge pipeline creates no commits. Skips count as successes, so a
re-run after a partial failure finishes the rest.

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

