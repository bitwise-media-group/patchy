# The patchy CLI

`patchy` works with the pipeline's custom resources from a terminal: list findings, read what an agent concluded, and
approve or suspend work.

It talks to the Kubernetes API with **your** kubeconfig — never through a controller, and never through the status
server. There is no patchy-specific auth, no separate endpoint to expose, and no service account acting on your behalf:
what you can do is exactly what your RBAC allows.

## Install

Binaries ship with each release, cosign-signed, for linux, macOS and windows:

```sh
# from a release archive
tar -xzf patchy_<version>_<os>_<arch>.tar.gz
install -m 0755 patchy /usr/local/bin/patchy

# or from source
go install github.com/bitwise-media-group/patchy/cmd/patchy@latest
```

The archive also carries `kubectl-patchy`. Put that on your `PATH` and every command below also works as
`kubectl patchy …`:

```sh
kubectl patchy get findings
```

Shell completion covers verbs, nouns, and the enumerated flag values (phases, severities, output formats):

```sh
patchy completion zsh > "${fpath[1]}/_patchy"
```

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

## Permissions

Each action is a **custom RBAC verb** on `findings.patchy.bitwisemedia.uk`, granted independently: holding `approve`
says nothing about `suspend`. To see yours:

```sh
patchy can-i                # the whole matrix
patchy can-i approve        # one verb; exit code answers, for shell conditionals
```

Two things enforce those verbs, and only one of them matters:

- The CLI runs a `SelfSubjectAccessReview` before writing. This is **ergonomics** — a fast, clear failure naming the
  verb you lack instead of an opaque server rejection. It carries no security weight and is trivially bypassed by not
  using the CLI.
- A **`ValidatingAdmissionPolicy`** in the cluster binds each spec field to its verb. This runs inside the API server's
  admission chain, so it applies identically to `patchy`, `kubectl edit`, `kubectl patch`, server-side apply and raw
  `curl`. This is the actual enforcement.

That second piece exists because Kubernetes RBAC has no notion of a field: `update` on findings grants the whole object.
Without the policy, letting a developer suspend a finding would also let them rewrite its severity or forge an approval.
See [Kustomize](deployment/kustomize.md) for the manifest and the role ladder.

!!! warning "Requires Kubernetes 1.30+"

    `ValidatingAdmissionPolicy` reached GA in 1.30. On an older cluster the policy will not install and
    enforcement degrades silently to "whoever holds `update` owns the whole resource". Check with
    `kubectl api-resources | grep validatingadmissionpolic`.

### Adding a field to FindingSpec

The policy has to enumerate the spec fields it freezes, because CEL cannot express "everything except these four". A new
field would otherwise become writable by anyone holding `update`. `TestAdmissionPolicyCovers‑ EveryFindingSpecField` in
`internal/action` reflects over the Go type and fails if a field is unaccounted for — when it fires, add the field to
the frozen-fields validation in `deploy/kustomize/base/admission-policy.yaml`.

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
