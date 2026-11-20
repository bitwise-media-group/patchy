## patchy mirror

Mirror upstream helm charts and OCI artifacts into a platform registry

### Synopsis

Maintain a vendored mirror store. This is a git tree where every charts/<name>/
or artifacts/<name>/ directory pins one upstream chart or OCI artifact. Each one
is vendored for review, locked by digest, checked against its publisher's
signatures, scanned, and published with your signature. The registries, signing
and scan defaults come from the patchy config (shared with `patchy build`). The
mirror: block adds update and sourceRegistryRewrites.

`upgrade` moves pins forward and regenerates the derived files. `sync` brings
the registries in line with the committed state. It is idempotent and never
replaces an existing tag. `validate` checks that the committed state is
consistent, without changing the tree. patchy never runs git: commits, branches
and PRs belong to the calling pipeline.

Flags can also come from PATCHY_MIRROR_* environment variables and the patchy
config's mirror: block.

The patchy config merges from, least to most preferred: /etc/patchy/config.<ext>,
~/.config/patchy/config.<ext>, then in the working directory mise/patchy.<ext>,
patchy/config.<ext>, .mise/patchy.<ext>, .patchy/config.<ext> and patchy.<ext>
(<ext> is yaml, yml, json or toml). Maps merge, lists replace, and null deletes
an inherited key.

### Options

```
  -C, --directory string    mirror store directory, relative to the working directory (default: the working directory)
  -h, --help[=true|false]   help for mirror
      --workers int         concurrent registry operations per stage (default 4)
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
* [patchy mirror add](patchy_mirror_add.md)	 - Scaffold a new chart or artifact entry
* [patchy mirror sync](patchy_mirror_sync.md)	 - Publish committed charts and images to the mirror registries
* [patchy mirror upgrade](patchy_mirror_upgrade.md)	 - Move pins to newer upstream versions and regenerate derived state
* [patchy mirror validate](patchy_mirror_validate.md)	 - Check that the committed store is current, verified and clean

