# The patchy CLI

`patchy` works with the pipeline's custom resources from a terminal: list findings, read what an agent concluded, and
approve or suspend work.

It talks to the Kubernetes API with **your** kubeconfig — never through a controller, and never through the status
server. There is no patchy-specific auth, no separate endpoint to expose, and no service account acting on your behalf:
what you can do is exactly what your RBAC allows.

This page is the tour. For every command, flag and default, generated from the binary itself, see the
[command reference](cli/patchy.md).

## Install

On macOS and linux, from the Homebrew tap:

```sh
brew install bitwise-media-group/tap/patchy
```

Otherwise, binaries ship with each release, cosign-signed, for linux, macOS and windows:

```sh
# from a release archive
tar -xzf patchy-cli_<version>_<os>_<arch>.tar.gz
install -m 0755 patchy /usr/local/bin/patchy

# or from source
go install github.com/bitwise-media-group/patchy/cmd/patchy@latest
```

The cask and the archive both carry `kubectl-patchy` — brew puts it on your `PATH` for you; from an archive, put it
there yourself. Either way every command below also works as `kubectl patchy …`:

```sh
kubectl patchy get findings
```

## Shell completion

Completion covers verbs, nouns, and the enumerated flag values (phases, severities, output formats). The cask installs
all of it; from an archive, install what your shell reads — the scripts ship pre-generated under `completions/`:

```sh
install -m 0644 completions/patchy.zsh "${fpath[1]}/_patchy"                     # zsh
install -m 0644 completions/patchy.bash /usr/local/etc/bash_completion.d/patchy  # bash
install -m 0644 completions/patchy.fish ~/.config/fish/completions/patchy.fish   # fish
```

`patchy completion <shell>` prints the same script, and adds `powershell`.

`kubectl patchy …` completes through a different mechanism, and needs one more file. kubectl never reads a plugin's own
completion script: it strips its own global flags, then looks for an executable named `kubectl_complete-<plugin>` on
your `PATH` and asks that for candidates. So the hook belongs in `bin`, not in a completion directory:

```sh
install -m 0755 completions/kubectl_complete-patchy /usr/local/bin/kubectl_complete-patchy
```

It is a one-line forward to `kubectl-patchy __complete`, so both spellings complete from the same command tree and
cannot drift. Requires kubectl 1.26 or newer; the cask installs it for you.

## Grammar

```text
patchy <verb> <noun> [name...] [flags]
```

Verbs and nouns are separate axes: every verb accepts any noun it makes sense for, so learning one verb teaches you all
of them. Nouns take the same short names the CRDs declare, which means `patchy get fnd` and `kubectl get fnd` always
mean the same thing.

| Noun            | Also accepts                                |
| --------------- | ------------------------------------------- |
| `finding`       | `findings`, `fnd`                           |
| `investigation` | `investigations`, `inv`                     |
| `remediation`   | `remediations`, `rem`                       |
| `findingrollup` | `findingrollups`, `fr`, `rollup`, `rollups` |
| `repository`    | `repositories`, `repo`, `repos`             |
| `integration`   | `integrations`                              |
| `forge`         | `forges`                                    |

`get` takes one more: `all`, meaning every kind in the table above. It is the CLI's spelling of `kubectl get patchy`
(every CRD declares the `patchy` category), and no other verb accepts it — there is nothing sensible to describe, review
or approve collectively.

## Global flags

```text
    --kubeconfig string    path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
    --context string       kubeconfig context to use
-n, --namespace string     namespace to work in (default: the context's, exactly as kubectl resolves it)
-A, --all-namespaces       work across every namespace
-o, --output string        table | wide | json | yaml | name | markdown   (default table)
    --no-color             disable colour and styling
    --request-timeout dur  timeout for a single API call (default 30s)
-v, --verbose              log what the CLI is doing to stderr
```

Namespace resolution follows kubectl's rules exactly, including its fallback to `default`, so the two tools never
disagree about where they are looking. If your findings live in `patchy`, either set that on your context or pass
`-n patchy`.

## Reading

```sh
patchy get findings
patchy get findings -o wide                      # adds the issue and pull-request links
patchy get findings --phase AwaitingApproval
patchy get findings --severity critical,high --sort-by severity
patchy get findings --awaiting                   # only findings you could act on right now
patchy get findings --repo billing --suspended
patchy get investigations --finding my-finding
```

Columns come from the CRDs' own print columns, so they match `kubectl get` and always will; `-o wide` adds the ones the
CRDs mark lower priority. Filters that map to labels (`--severity`, `--source`, `--finding`, `-l`) run on the API
server; the rest (`--phase`, `--verdict`, `--repo`, `--suspended`, `--awaiting`) need the object and run locally.

`patchy get all` is the whole pipeline in one screen — every kind, each in its own table, in the order the pipeline uses
them:

```text
Findings
NAME    REPO      SEVERITY   PRIORITY   PHASE    VERDICT     AGE
fnd-1   billing   high       high       Queued   remediate   4h

Investigations
NAME          FINDING   ATTEMPT   STATE      VERDICT     AGE
fnd-1-inv-1   fnd-1     1         Complete   remediate   3h

Forges
NAME   PROVIDER   READY   AGE
gh     github     True    9d
```

Kinds with nothing in them are left out rather than printed as an empty table. `all` lists, and only lists: it takes no
names, and it refuses the finding-only filters (`--phase`, `--verdict`, `--repo`, `--suspended`, `--awaiting`,
`--finding`) rather than narrow one table and leave the rest whole. The label filters (`-l`, `--severity`, `--source`)
mean the same thing on every kind, so those do apply. `-o json` and `-o yaml` give you one stream of every object;
`-o name` gives you fully-qualified references you can pipe back into `kubectl`.

```sh
patchy describe finding my-finding               # state, timeline, owners, alerts, runs, spend
patchy describe investigation my-finding-inv-1
```

## Reviewing an agent's work

```sh
patchy review finding my-finding                     # both stages together
patchy review investigation --finding my-finding     # the latest attempt
patchy review investigation --finding my-finding --attempt 2
patchy review remediation my-finding-rem-1
```

On a terminal the report is rendered for reading. Piped, or with `-o markdown`, you get the markdown the agent actually
wrote — so pasting it into a ticket loses nothing:

```sh
patchy review finding my-finding -o markdown > report.md
```

`--raw` keeps the machine frontmatter, which is a contract between the investigate and remediate stages and is stripped
by default.

## Opening the human-facing page

```sh
patchy browse finding my-finding          # the tracking issue
patchy browse remediation my-finding-rem-1 # the pull request
patchy browse finding my-finding --print-url
```

The verb is `browse` rather than `open` because `Opened` is a real phase — `patchy open finding` would read like a state
transition. `patchy review … --web` is the same behaviour without leaving the review.

## Acting on a finding

```sh
patchy approve  finding my-finding [--note "shipping despite the break"]
patchy suspend  finding my-finding
patchy resume   finding my-finding
patchy retry    finding my-finding
patchy expedite finding my-finding
```

Every action writes to the finding's **spec only**. A controller observes the change and moves the phase — the CLI never
writes status and never transitions a finding itself, which is what keeps each phase edge single-writer.

Actions are idempotent. Approving an already-approved finding succeeds and changes nothing, so re-running a script is
safe:

```sh
patchy suspend finding my-finding --dry-run                              # report, write nothing
patchy suspend finding -l patchy.bitwisemedia.uk/severity=critical -y    # bulk, no prompt
```

Bulk operations prompt above one finding unless you pass `-y`, report each finding individually, and exit non-zero if
any failed — one unavailable finding never stops the rest.

## Backfilling an integration

One action acts on an Integration rather than a finding: the
[manual backfill](integrations/sources/github.md#the-manual-backfill), which lists the provider's open alerts and
ingests the ones that predate webhook coverage.

```sh
patchy backfill gh                                      # the credential's full scope
patchy backfill gh --repo acme/                         # one owner
patchy backfill gh --repo acme/shop --repo acme/billing # exact repositories (required with a PAT)
```

Like every action it writes spec only (`spec.backfill`); the integration-controller runs the walk on its next reconcile
and reports on `status.backfill` — watch for `truncated`, which means the page budget ran out and a narrower `--repo`
prefix is needed to reach the rest.

## Testing a generic integration

The `dev` commands are the one part of the CLI that never touches a cluster: a local test harness for authors of
[generic integrations](integrations/sources/generic.md).

```sh
patchy dev generic --secret dev-secret --enhance-url http://127.0.0.1:9000/enhance
```

hosts the real generic webhook receiver on your workstation — the same HMAC authentication, deduplication, and
validation the integration-controller runs — retains ingested findings in memory, and drives the enhancer call and
resolver write-back at your endpoints, so one signed POST tests every exchange of the contract. For a process that is
only an enhancer or resolver, `patchy dev enhance` / `patchy dev resolve` fire a single signed exchange from a findings
payload file, no server involved.

Uniquely in the CLI, every `dev` flag also resolves from `PATCHY_DEV_*` environment variables and an optional
`.patchy.yaml` in the working directory (flag beats environment beats file). See the
[generic integration guide](integrations/sources/generic.md#testing-your-integration-locally) for the full walkthrough.

## Maintaining a chart and image mirror

The `mirror` commands are the other cluster-free group: they maintain a vendored mirror store — a git repository where
`mirror.yaml` holds global defaults and every `charts/<name>/` or `artifacts/<name>/` directory pins one upstream helm
chart or OCI artifact, vendored for PR review, digest-locked, provenance-verified, scanned, and published signed to a
platform registry.

```sh
patchy mirror upgrade --check -o json   # what would move, as data
patchy mirror upgrade --all             # move pins, regenerate everything derived
patchy mirror validate --all            # the CI gate: current, verified, clean
patchy mirror sync --all                # converge the registry, idempotently
```

Signing, verification, and image scanning shell out to external binaries rather than linking them: `sync` and `validate`
need `cosign` (v3 or later) on `PATH`, and the image scanners are all opt-in shell-outs — enable `scan.scanners.osv`
(`osv-scanner` v2) or `scan.scanners.grype` (`grype`) in `mirror.yaml`, since an image scan with neither enabled is an
error; `scan.enabled: false` turns image scanning off deliberately. `kubescape` remains the optional configuration
scanner. Existing stores that relied on the old built-in default should add `scan.scanners.osv.enabled: true` and
install `osv-scanner` (mise/brew).

`upgrade` mutates files and nothing else — patchy never runs git, so branches, commits, and pull requests belong to the
calling pipeline. Entries that share a `lockstep` group bump together, holding at the lowest version every member has
published. `sync` never replaces an existing chart tag and skips anything already current and signed, so re-runs are
safe. `validate` regenerates the derived state out-of-tree and byte-compares it, which is why upgrade is the only verb
allowed to consult the wall clock (tracked-tag cooldowns, allowlist expiry stamping).

Like `dev`, the `mirror` flags also resolve from `PATCHY_MIRROR_*` environment variables and `.patchy.yaml` (`mirror:`
block); `-C` points at a store checkout from anywhere.

## Permissions

Each action is a **custom RBAC verb**, granted independently: holding `approve` says nothing about `suspend`. The
per-finding verbs (`approve`, `retry`, `expedite`, `suspend`, `resume`) live on `findings.patchy.bitwisemedia.uk`; the
integration-scoped ones (`backfill`, `replay`, `reset`) on `integrations.patchy.bitwisemedia.uk`. To see yours:

```sh
patchy can-i                # the whole matrix, findings and integrations
patchy can-i approve        # one verb; exit code answers, for shell conditionals
patchy can-i backfill       # integration-scoped verbs resolve on integrations
```

Two things enforce those verbs, and only one of them matters:

- The CLI runs a `SelfSubjectAccessReview` before writing. This is **ergonomics** — a fast, clear failure naming the
  verb you lack instead of an opaque server rejection. It carries no security weight and is trivially bypassed by not
  using the CLI.
- A **`ValidatingAdmissionPolicy`** in the cluster binds each spec field to its verb. This runs inside the API server's
  admission chain, so it applies identically to `patchy`, `kubectl edit`, `kubectl patch`, server-side apply and raw
  `curl`. This is the actual enforcement. There are two policies: one on findings, one on integrations (gating
  `spec.backfill`/`spec.replay`/`spec.reset` while leaving the operator's configuration surface freely updatable).

That second piece exists because Kubernetes RBAC has no notion of a field: `update` on findings grants the whole object.
Without the policy, letting a developer suspend a finding would also let them rewrite its severity or forge an approval.
See [Kustomize](deployment/kustomize.md) for the manifest and the role ladder.

!!! warning "Requires Kubernetes 1.30+"

    `ValidatingAdmissionPolicy` reached GA in 1.30. On an older cluster the policy will not install and
    enforcement degrades silently to "whoever holds `update` owns the whole resource". Check with
    `kubectl api-resources | grep validatingadmissionpolic`.

Because the policy enumerates the spec fields it freezes, contributors adding a field to `FindingSpec` must extend it —
see [Extending](extending.md#adding-a-field-to-findingspec).

## Exit codes

| Code | Meaning                                                                   |
| ---- | ------------------------------------------------------------------------- |
| `0`  | success                                                                   |
| `1`  | runtime failure — unreachable cluster, bad response                       |
| `2`  | usage error                                                               |
| `3`  | the named resource does not exist                                         |
| `4`  | RBAC refused, or the action is unavailable in the finding's current phase |

## A triage session

```sh
patchy get findings --awaiting -o wide      # what needs a human
patchy review finding <name>                # what the agent concluded, and why
patchy browse finding <name>                # the tracking issue, if you want the thread
patchy approve finding <name>               # release the hold
```

## Output and piping

Rendered reports use the dark theme by default. On a light terminal, set the same variable `glow` and the other charm
tools read:

```sh
export GLAMOUR_STYLE=light     # or dark, dracula, tokyo-night, notty, ascii
```

There is no automatic detection: probing a terminal's background needs a query/response round trip that only an
interactive event loop can service, and `patchy` is a one-shot command. Tables are unaffected — they use the ANSI 0-15
palette, so they inherit whatever theme your terminal already has.

`stdout` carries data, `stderr` carries narration — so `-v` never corrupts a pipe, and "no findings found" goes to
stderr rather than into your `jq`. Styling turns itself off whenever stdout is not a terminal, and also honours
`--no-color`, `NO_COLOR`, and `TERM=dumb`.

```sh
patchy get findings -o json | jq '.items[] | select(.status.priority == "critical") | .metadata.name'
patchy get findings -o name | xargs -n1 patchy describe finding
```
