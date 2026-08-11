## patchy mirror upgrade

Move pins to newer upstream versions and regenerate derived state

### Synopsis

Converge entries onto their target versions: resolve the newest upstream
version satisfying each entry's constraint, splice the pin, re-pick tracked
image tags past their cooldown, re-vendor, re-render, and regenerate the
digest locks. A fully-converged entry is a clean no-op.

Entries sharing a lockstep group bump together: the group target is the
lowest of the members' newest satisfying versions, holding the bump until
upstream has published every member's tag.

--check reports the plan as data without touching the tree (exit 0 with an
empty groups list when everything is current). patchy never commits: the
calling pipeline turns the mutated tree into branches and PRs.

```
patchy mirror upgrade [name]... [flags]
```

### Examples

```
  patchy mirror upgrade --check -o json
  patchy mirror upgrade --all
  patchy mirror upgrade --group flux --to 0.57.0
  patchy mirror upgrade opentelemetry-collector
```

### Options

```
      --all            upgrade every entry in the store
      --check          report the update plan without touching the tree
      --group string   upgrade one lockstep group
  -h, --help           help for upgrade
      --to string      target version (default: newest satisfying the constraint)
```

### Options inherited from parent commands

```
  -A, --all-namespaces             work across every namespace
      --context string             kubeconfig context to use
  -C, --directory string           mirror store directory (walks up to mirror.yaml; default: the working directory)
      --kubeconfig string          path to the kubeconfig file
  -n, --namespace string           namespace to work in (default: the context's)
      --no-color                   disable colour and styling
  -o, --output string              output format: table, wide, json, yaml, name, or markdown (default "table")
      --request-timeout duration   timeout for a single API call (default 30s)
  -v, --verbose                    log what the CLI is doing to stderr
      --workers int                concurrent registry operations per stage (default 4)
```

### SEE ALSO

* [patchy mirror](patchy_mirror.md)	 - Mirror upstream helm charts and OCI artifacts into a platform registry

