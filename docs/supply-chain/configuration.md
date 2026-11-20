# Configuration

The [patchy CLI](../operate/cli.md) command groups that run without a cluster (`dev`, `mirror` and `build`) share one
patchy configuration, built from several files in layers. The cluster verbs never read it. They use your kubeconfig.

## Where it is read from

patchy merges the configuration from up to seven locations, listed here from least to most preferred. A project can
refine a system-wide or per-user default this way:

| #   | Location                         | Typical use                                     |
| --- | -------------------------------- | ----------------------------------------------- |
| 1   | `/etc/patchy/config.<ext>`       | machine-wide defaults (a CI image)              |
| 2   | `~/.config/patchy/config.<ext>`  | your own defaults (a dev secret file)           |
| 3   | `<workdir>/mise/patchy.<ext>`    | project config beside a `mise/` dir             |
| 4   | `<workdir>/patchy/config.<ext>`  | project config in its own directory             |
| 5   | `<workdir>/.mise/patchy.<ext>`   | project config beside a `.mise/` dir            |
| 6   | `<workdir>/.patchy/config.<ext>` | hidden project config                           |
| 7   | `<workdir>/patchy.<ext>`         | the plain project file (`mirror add` writes it) |

`<ext>` is `yaml`, `yml`, `json` or `toml`, and `<workdir>` is the directory you run patchy in. If one location has
files in two formats, patchy cannot tell which to use and refuses to run. A file that two locations point to is read
only once. Every command lists the layers it loaded on stderr
(`patchy: using ~/.config/patchy/config.toml, patchy.yaml`).

## How layers merge

The layers merge key by key, and the more specific layer wins:

- Maps merge recursively.
- Lists and scalars are replaced whole.
- An explicit `null` deletes an inherited key. For example, a project can drop a signing block it inherits from `/etc`:

```yaml
signing: null # this project publishes with each registry's own signing block
scan:
  failOn: [CRITICAL] # replaces the inherited list; the rest of scan: still merges
```

Relative paths in any layer (`mirror.directory`, a build entry's `dockerfile`, `context` and `path`) resolve against the
working directory, not against the file that holds them. Keys are strict. A typo fails with an error that names the file
it is in (`~/.config/patchy/config.toml: 'build.images[0]' has invalid keys: dockerfil`).

Some keys can also be set by a flag: `dev.*`, `mirror.directory` and `mirror.workers`. For those, the usual order
applies on top of the files. The flag wins, then the `PATCHY_*` environment variable, then the merged files, then the
flag's default.

## The file's shape

Blocks that `mirror` and `build` share sit at the top level. Each command group then has its own block:

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

patchy no longer reads either file. A `.patchy.yaml` in the working directory is ignored with a warning. Move its
contents, unchanged, into `patchy.yaml` or into any location above. A `mirror.yaml` left at a mirror root makes every
`mirror` command fail until you migrate it. See [Migrating from `mirror.yaml`](mirror.md#migrating-from-mirroryaml) for
the steps.

## Source policy

`mirror.policy` and `build.policy` restrict where each command may pull from. A policy block marked `enforced: true`
behaves unlike every other key: merging cannot remove it. patchy evaluates it as written in the layer that declares it.
A more preferred layer can tighten it, but can never loosen or delete it. See [Restricting sources](source-policy.md).

## Scanning policy

The `scan` block has the same shape everywhere. In the config it holds the defaults: which scanners run, which
severities block, and how long an allowlist acceptance may last. Every chart manifest, artifact manifest and build entry
can override it field by field. See [Scanning and allowlists](scanning.md) for the fields, their built-in defaults, what
`enabled: auto` means for each kind of entry, and the allowlist format.
