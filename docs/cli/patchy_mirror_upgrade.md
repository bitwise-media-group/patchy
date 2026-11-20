## patchy mirror upgrade

Move pins to newer upstream versions and regenerate derived state

### Synopsis

Move entries to their target versions. For each entry, upgrade finds the
newest upstream version that satisfies its constraint and writes the new pin.
It re-picks tracked image tags that are past their cooldown, re-vendors,
re-renders, and regenerates the digest locks. An entry that is already up to
date is left unchanged.

Entries in the same lockstep group move together. The group's target is the
lowest of the members' newest satisfying versions, so the bump waits until
upstream has published every member's tag.

--check reports the plan as data without changing the tree. It exits 0 with an
empty groups list when everything is current. patchy never commits: the
calling pipeline turns the changed tree into branches and PRs.

--pinned uses the pins already in the tree. It is for an external updater
(Renovate) that moved chart.version or a tracked tag itself. No target is
resolved and no tracked tag is re-picked, but every pin must satisfy its
versionConstraint and every lockstep member must share it.

```
patchy mirror upgrade [name]... [flags]
```

### Examples

```
  patchy mirror upgrade --check -o json
  patchy mirror upgrade --all
  patchy mirror upgrade --group flux --to 0.57.0
  patchy mirror upgrade --pinned kyverno
  patchy mirror upgrade opentelemetry-collector
```

### Options

```
      --all[=true|false]      upgrade every entry in the store
      --check[=true|false]    report the update plan without touching the tree
      --group string          upgrade one lockstep group
  -h, --help[=true|false]     help for upgrade
      --pinned[=true|false]   use the pins already in the tree: check them, but pick no new versions
      --to string             target version (default: newest satisfying the constraint)
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

