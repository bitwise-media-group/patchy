## patchy mirror add

Scaffold a new chart or artifact entry

### Synopsis

Create the entry directory for a new mirrored chart or OCI artifact: detect
what --url points at (an https:// helm repository, an oci:// chart
repository, or a bare artifact reference), pin the newest stable version,
default the constraint to stay-in-major, and scaffold manifest.yaml.

The name defaults to the URL's last path segment. On first use in an empty
directory, add also writes a starter mirror.yaml with commented defaults.
Nothing is vendored and nothing is committed — review the scaffold, then run
`patchy mirror upgrade <name>` to vendor and lock it.

```
patchy mirror add [name] [flags]
```

### Examples

```
  patchy mirror add --url oci://ghcr.io/open-telemetry/opentelemetry-helm-charts/opentelemetry-collector
  patchy mirror add dex --url https://charts.dexidp.io
  patchy mirror add runner-bundle --url ghcr.io/example/bundle --type artifact
```

### Options

```
      --constraint string   version constraint (default: stay within the pinned major)
  -h, --help                help for add
      --lockstep string     lockstep group this entry bumps with
      --type string         force the entry type: chart or artifact (default: detect)
      --url string          upstream location: https:// helm repo, oci:// chart repo, or artifact ref
      --version string      pin this version instead of the newest stable
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

