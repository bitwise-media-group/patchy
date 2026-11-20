# source-controller

The `Forge` and `Repository` reconcilers, plus the artifact server. The controller:

- validates forge credentials
- resolves each `Repository` to its covering `Forge`
- pins the head SHA once
- downloads the forge's tarball archive at that SHA over plain HTTP (no controller image carries a git binary)
- serves the tarball from an in-cluster artifact endpoint

Agent pods fetch from that endpoint **without credentials**. The URL carries an unguessable 128-bit id, and the Job pins
the sha256 digest.

```sh
source-controller serve --namespace patchy --artifact-addr :9790
```

## Flags

The [shared flags](index.md#shared-flags), plus:

| Flag                   | Env                         | Default              | Purpose                                                                            |
| ---------------------- | --------------------------- | -------------------- | ---------------------------------------------------------------------------------- |
| `--artifact-addr`      | `PATCHY_ARTIFACT_ADDR`      | `:9790`              | Listen address of the artifact server                                              |
| `--artifact-base-url`  | `PATCHY_ARTIFACT_BASE_URL`  | in-cluster Service   | Base URL minted into Repository statuses for agent fetches                         |
| `--artifact-dir`       | `PATCHY_ARTIFACT_DIR`       | `/data/artifacts`    | Directory the artifact tarballs are stored in                                      |
| `--max-artifact-bytes` | `PATCHY_MAX_ARTIFACT_BYTES` | `1073741824` (1 GiB) | Largest repository tarball stored; larger repositories stall (`Stalled` condition) |

Only the optional [evaluation-controller](evaluation-controller.md) uses the internal workspace endpoint. It streams
evolve's workspace bundles to it:

| Flag                           | Env                                 | Default             | Purpose                                                                                     |
| ------------------------------ | ----------------------------------- | ------------------- | ------------------------------------------------------------------------------------------- |
| `--artifact-internal-addr`     | `PATCHY_ARTIFACT_INTERNAL_ADDR`     | none (disabled)     | Listen address of the internal workspace-upload endpoint; the deployments use `:9791`       |
| `--internal-upload-token-file` | `PATCHY_INTERNAL_UPLOAD_TOKEN_FILE` | none                | File holding a shared bearer token uploads must present (defence in depth on NetworkPolicy) |
| `--max-workspace-bytes`        | `PATCHY_MAX_WORKSPACE_BYTES`        | `67108864` (64 MiB) | Largest workspace bundle accepted                                                           |
| `--workspace-retention`        | `PATCHY_WORKSPACE_RETENTION`        | `168h` (7 days)     | Remove bundles not fetched for this long; `0` keeps them forever                            |

You only need to set `--artifact-base-url` when the Service name differs from the default
`http://patchy-source-controller.<namespace>.svc.cluster.local:<port>`. The deployments leave it unset.

## Behavior

- **Forge reconciler**: validates each Forge's referenced credential Secret on its `spec.interval` and maintains its
  `Ready` condition. Matching checks host equality first, then the optional `orgs` allowlist, then the optional
  repository-name regexes. The most-constrained matching Forge wins. An ambiguous match stalls the Finding with
  `ForgeResolved: False` / `Ambiguous`.
- **Repository reconciler**: the investigation-controller's gate creates each `Repository`. This reconciler resolves it
  to its Forge, pins its head SHA **once**, and downloads the tarball at that commit. The pin guarantees that
  investigation and remediation see the same code.
- **Artifact server**: serves the stored tarballs on `--artifact-addr`. The Deployment stores them in an `emptyDir`.
  They can be rebuilt from the pinned SHA, so after a pod restart the controller downloads them again. The Service is
  reachable only inside the cluster, and the NetworkPolicies restrict the port to the agent namespace.
- **Workspace blobs**: when `--artifact-internal-addr` is set, the internal endpoint accepts sha256-named workspace
  bundles. It verifies each digest before caching the bundle, then serves it back to agent pods from the artifact
  endpoint. A retention sweep removes bundles nobody has fetched for `--workspace-retention`.
