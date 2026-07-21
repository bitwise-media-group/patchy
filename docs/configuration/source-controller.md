# source-controller

The `Forge` and `Repository` reconcilers, plus the artifact server. It validates forge credentials, resolves each
`Repository` to its covering `Forge`, pins the head SHA exactly once, downloads the forge's tarball archive at that SHA
(pure HTTP — no controller image carries a git binary), and serves it from an in-cluster artifact endpoint the agent
pods fetch **credential-lessly**: the URL carries an unguessable 128-bit id and the Job pins the sha256 digest.

```sh
source-controller serve --namespace patchy --artifact-addr :9790
```

## Flags

The [shared flags](index.md#shared-flags-all-five-controllers), plus:

<div class="nowrap-first" markdown>

| Flag                   | Env                         | Default              | Purpose                                                                            |
| ---------------------- | --------------------------- | -------------------- | ---------------------------------------------------------------------------------- |
| `--artifact-addr`      | `PATCHY_ARTIFACT_ADDR`      | `:9790`              | Listen address of the artifact server                                              |
| `--artifact-base-url`  | `PATCHY_ARTIFACT_BASE_URL`  | in-cluster Service   | Base URL minted into Repository statuses for agent fetches                         |
| `--artifact-dir`       | `PATCHY_ARTIFACT_DIR`       | `/data/artifacts`    | Directory the artifact tarballs are stored in                                      |
| `--max-artifact-bytes` | `PATCHY_MAX_ARTIFACT_BYTES` | `1073741824` (1 GiB) | Largest repository tarball stored; larger repositories stall (`Stalled` condition) |

</div>

`--artifact-base-url` only needs setting when the Service name differs from the default
`http://patchy-source-controller.<namespace>.svc.cluster.local:<port>` — the deployments leave it unset.

## Behavior

- **Forge reconciler** — validates each Forge's referenced credential Secret on its `spec.interval` and maintains its
  `Ready` condition. Matching is host equality, then the optional `orgs` allowlist, then the optional repository-name
  regexes; the most-constrained matching Forge wins, and an ambiguous match stalls the Finding with
  `ForgeResolved: False` / `Ambiguous`.
- **Repository reconciler** — created by the investigation-controller's gate, a `Repository` is resolved to its Forge,
  its head SHA pinned **once**, and the tarball downloaded at exactly that commit. The pin is what guarantees
  investigation and remediation see the same code.
- **Artifact server** — serves the stored tarballs on `--artifact-addr`. The Deployment stores them in an `emptyDir`:
  they are reproducible from the pinned SHA, so a pod restart just re-downloads. The Service is in-cluster only by
  design, and the NetworkPolicies restrict the port to the agent namespace.
