## patchy mirror add

Scaffold a new chart or artifact entry

### Synopsis

Create the entry directory for a new mirrored chart or OCI artifact: detect
what --url points at (an https:// helm repository, an oci:// chart
repository, or a bare artifact reference), pin the newest stable version,
default the constraint to stay-in-major, and scaffold manifest.yaml.

The name defaults to the URL's last path segment. When the working directory
has no patchy config yet, add also writes a starter patchy.yaml with
commented defaults (registries, signing, scan, mirror and a build example).
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
  -h, --help[=true|false]   help for add
      --lockstep string     lockstep group this entry bumps with
      --type string         force the entry type: chart or artifact (default: detect)
      --url string          upstream location: https:// helm repo, oci:// chart repo, or artifact ref
      --version string      pin this version instead of the newest stable
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

