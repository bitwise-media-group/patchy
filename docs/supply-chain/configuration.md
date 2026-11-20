# Configuration

The cluster-free command groups of the [patchy CLI](../operate/cli.md) (`dev`, `mirror` and `build`) share one layered
patchy configuration. The cluster verbs never read it; they use your kubeconfig.

## Where it is read from

It merges from up to seven locations, least to most preferred, so a system-wide or per-user default can be refined by a
project:

| #   | Location                         | Typical use                                     |
| --- | -------------------------------- | ----------------------------------------------- |
| 1   | `/etc/patchy/config.<ext>`       | machine-wide defaults (a CI image)              |
| 2   | `~/.config/patchy/config.<ext>`  | your own defaults (a dev secret file)           |
| 3   | `<workdir>/mise/patchy.<ext>`    | project config beside a `mise/` dir             |
| 4   | `<workdir>/patchy/config.<ext>`  | project config in its own directory             |
| 5   | `<workdir>/.mise/patchy.<ext>`   | project config beside a `.mise/` dir            |
| 6   | `<workdir>/.patchy/config.<ext>` | hidden project config                           |
| 7   | `<workdir>/patchy.<ext>`         | the plain project file (`mirror add` writes it) |

`<ext>` is `yaml`, `yml`, `json` or `toml`, and `<workdir>` is the directory you run patchy in. Two formats at one
location are ambiguous and refused; a file two locations resolve to is read once. Every command narrates the layers it
loaded on stderr (`patchy: using ~/.config/patchy/config.toml, patchy.yaml`).

## How layers merge

The layers merge key by key: **maps merge recursively, lists and scalars are replaced whole** by the more specific
layer, and an explicit `null` deletes an inherited key — the way a project drops, say, a signing block from `/etc`:

```yaml
signing: null # this project publishes with each registry's own signing block
scan:
  failOn: [CRITICAL] # replaces the inherited list; the rest of scan: still merges
```

Relative paths in any layer (`mirror.directory`, a build entry's `dockerfile`, `context` and `path`) resolve against the
working directory, not the file that holds them. Keys are strict: a typo fails against the file that carries it
(`~/.config/patchy/config.toml: 'build.images[0]' has invalid keys: dockerfil`). For the flag-backed keys (`dev.*`,
`mirror.directory`, `mirror.workers`) the usual precedence still holds on top: flag, then `PATCHY_*` environment
variable, then the merged files, then the flag default.

## The file's shape

The blocks `mirror` and `build` share sit at the top level, then one block per command group:

```yaml
registries: # where mirror and build publish (each with an optional signing override)
  - name: ghcr
    url: ghcr.io/org/platform
signing: # the default signing block
  provider: keyless
  keyless:
    certificateIdentity: https://github.com/org/repo/.github/workflows/publish.yaml@refs/heads/main
scan: # scanner roster + the defaults every entry inherits (below)
  scanners:
    osv: { enabled: true }
mirror: # directory, workers, update.cooldownDays, sourceRegistryRewrites, policy
  update: { cooldownDays: 3 }
build: {} # see supply-chain/build.md; build.policy restricts base images
dev: {} # the dev command flags, by name
```

## Migrating from `mirror.yaml` and `.patchy.yaml`

Both files are gone. A `.patchy.yaml` in the working directory is ignored with a warning: move its contents into
`patchy.yaml`, or into any location above, unchanged. A `mirror.yaml` left at a mirror root fails every `mirror` command
until it is migrated; see [Migrating from `mirror.yaml`](mirror.md#migrating-from-mirroryaml) for the steps.

## Source policy

`mirror.policy` and `build.policy` restrict where each command may pull from. Unlike every other key, a policy block
marked `enforced: true` does not merge away: it is evaluated as written in the layer that declares it, so a more
preferred layer can tighten it but never loosen or delete it. See [Restricting sources](source-policy.md).

## Scanning policy

One `scan` block shape is used everywhere: in the config it holds the defaults (which scanners run, which severities
block, how long an allowlist acceptance may last), and every chart manifest, artifact manifest and build entry overrides
it field by field. See [Scanning and allowlists](scanning.md) for the fields, their built-in defaults, what
`enabled: auto` means for each kind of entry, and the allowlist format.
