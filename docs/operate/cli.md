# The patchy CLI

`patchy` works with the pipeline's custom resources from a terminal: list findings, read what an agent concluded, and
approve or suspend work.

It talks to the Kubernetes API with **your** kubeconfig. It never goes through a controller or the status server. There
is no patchy-specific auth, no separate endpoint to expose, and no service account acting for you. You can do what your
RBAC allows, and nothing more.

This page is the tour. For every command, flag and default, generated from the binary itself, see the
[command reference](../cli/patchy.md).

## Install

On macOS and linux, from the Homebrew tap:

```sh
brew install bitwise-media-group/tap/patchy
```

Otherwise, binaries ship with each release for linux, macOS and windows. They are cosign-signed on linux and windows,
and Developer ID-signed and notarized on macOS, so a downloaded `patchy` runs without a Gatekeeper prompt:

```sh
# from a release archive
tar -xzf patchy-cli_<version>_<os>_<arch>.tar.gz
install -m 0755 patchy /usr/local/bin/patchy

# or from source
go install github.com/bitwise-media-group/patchy/cmd/patchy@latest
```

The cask and the archive both include `kubectl-patchy`. brew puts it on your `PATH` for you. From an archive, put it
there yourself. Either way, every command below also works as `kubectl patchy …`:

```sh
kubectl patchy get findings
```

## Shell completion

Completion covers verbs, nouns, and the fixed lists of flag values (phases, severities, output formats). The cask
installs all of it. From an archive, install the script your shell reads. The scripts ship pre-generated under
`completions/`:

```sh
install -m 0644 completions/patchy.zsh "${fpath[1]}/_patchy"                     # zsh
install -m 0644 completions/patchy.bash /usr/local/etc/bash_completion.d/patchy  # bash
install -m 0644 completions/patchy.fish ~/.config/fish/completions/patchy.fish   # fish
```

`patchy completion <shell>` prints the same script, and adds `powershell`.

`kubectl patchy …` completes through a different mechanism and needs one more file. kubectl never reads a plugin's own
completion script. Instead it strips its own global flags, then looks on your `PATH` for an executable named
`kubectl_complete-<plugin>` and asks it for candidates. So this hook goes in a `bin` directory rather than a completion
directory:

```sh
install -m 0755 completions/kubectl_complete-patchy /usr/local/bin/kubectl_complete-patchy
```

It is a one-line forward to `kubectl-patchy __complete`, so both spellings complete from the same command tree and
cannot drift apart. It requires kubectl 1.26 or newer. The cask installs it for you.

## Grammar

```text
patchy <verb> <noun> [name...] [flags]
```

Verbs and nouns combine freely. Every verb accepts any noun it makes sense for, so once you learn a verb you can use it
on every noun. Nouns take the same short names the CRDs declare, so `patchy get fnd` and `kubectl get fnd` always mean
the same thing.

| Noun              | Also accepts                                |
| ----------------- | ------------------------------------------- |
| `finding`         | `findings`, `fnd`                           |
| `investigation`   | `investigations`, `inv`                     |
| `remediation`     | `remediations`, `rem`                       |
| `findingrollup`   | `findingrollups`, `fr`, `rollup`, `rollups` |
| `repository`      | `repositories`, `repo`, `repos`             |
| `integration`     | `integrations`                              |
| `backfillrequest` | `backfillrequests`, `bfr`                   |
| `forge`           | `forges`                                    |

`get` accepts one more noun: `all`, which means every kind in the table above. No other verb accepts it, because there
is nothing sensible to describe, review or approve as a group. It is close to `kubectl get patchy` (every CRD declares
the `patchy` category) but not identical. `kubectl get patchy` also lists the `Evaluation` and `EvaluationUnit` kinds of
[remote evaluation](../concepts/evaluation.md), and the CLI has no nouns for those.

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

Namespace resolution follows kubectl's rules, including its fallback to `default`, so the two tools always look in the
same place. If your findings live in `patchy`, either set that on your context or pass `-n patchy`.

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

Columns come from the CRDs' own print columns, so they match `kubectl get` and always will. `-o wide` adds the ones the
CRDs mark as lower priority. Filters that map to labels (`--severity`, `--source`, `--finding`, `-l`) run on the API
server. The others (`--phase`, `--verdict`, `--repo`, `--suspended`, `--awaiting`) need the full object, so they run
locally.

`patchy get all` shows every kind on one screen, each in its own table, in the order the pipeline uses them:

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

Kinds with nothing in them are left out instead of being printed as empty tables. `all` only lists. It takes no names.
It also refuses the finding-only filters (`--phase`, `--verdict`, `--repo`, `--suspended`, `--awaiting`, `--finding`),
because they would narrow one table and leave the rest unfiltered. The label filters (`-l`, `--severity`, `--source`)
mean the same thing on every kind, so those do apply. `-o json` and `-o yaml` give you one stream of every object.
`-o name` gives you fully qualified references you can pipe back into `kubectl`.

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

On a terminal, the report is rendered for reading. When you pipe it, or pass `-o markdown`, you get the markdown the
agent wrote, so pasting it into a ticket loses nothing:

```sh
patchy review finding my-finding -o markdown > report.md
```

`--raw` keeps the machine frontmatter. That frontmatter is a contract between the investigate and remediate stages, and
it is stripped by default.

### Transcripts, patches and full reports

`review` can also print one run's artifacts instead of its report summary:

```sh
patchy review investigation --finding my-finding --transcript | less   # the conversation
patchy review investigation --finding my-finding --transcript --raw    # as JSONL
patchy review remediation --finding my-finding --patch | git am        # the change itself
patchy review remediation --finding my-finding --report-full           # the untruncated report
patchy review remediation --finding my-finding --patch --print-url     # a 15-minute presigned URL
```

A run that was [archived](../deployment/archival.md) is read straight from the bucket with **your own** cloud
credentials (the AWS default chain or Google ADC); the run's status says where, so the CLI needs no other configuration.
Each object is checked against the sha256 recorded on the run before it is printed. A transcript that was not archived
is read from its ConfigMap instead, so `--transcript` works on every cluster while the finding exists. `--patch` and
`--report-full` need an archived run.

## Opening the human-facing page

```sh
patchy browse finding my-finding          # the tracking issue
patchy browse remediation my-finding-rem-1 # the pull request
patchy browse finding my-finding --print-url
```

The verb is `browse` rather than `open` because `Opened` is a real phase, and `patchy open finding` would read like a
state transition. `patchy review … --web` opens the same page from the `review` command.

## Acting on a finding

```sh
patchy approve  finding my-finding [--note "shipping despite the break"]
patchy suspend  finding my-finding
patchy resume   finding my-finding
patchy retry    finding my-finding
patchy expedite finding my-finding
```

Every action writes to the finding's **spec only**. A controller sees the change and moves the phase. The CLI never
writes status and never moves a finding to a new phase itself. That keeps a single writer for each phase change.

Actions are idempotent: approving an already-approved finding succeeds and changes nothing. So re-running a script is
safe:

```sh
patchy suspend finding my-finding --dry-run                              # report, write nothing
patchy suspend finding -l patchy.bitwisemedia.uk/severity=critical -y    # bulk, no prompt
```

Bulk operations ask for confirmation when they cover more than one finding, unless you pass `-y`. They report each
finding separately and exit non-zero if any failed. One unavailable finding never stops the rest.

## Backfilling an integration

One action works on an Integration rather than a finding: the
[manual backfill](../integrations/sources/github.md#the-manual-backfill). It lists the provider's open alerts and
ingests the ones raised before webhook coverage started.

```sh
patchy backfill gh                                      # the credential's full scope
patchy backfill gh --repo acme/                         # one owner
patchy backfill gh --repo acme/shop --repo acme/billing # exact repositories (required with a PAT)
```

Each run files one `BackfillRequest` (named `backfill-<random>`, labelled with its integration) and prints its name. The
integration-controller walks requests one at a time and records the outcome on the request itself. The CLI refuses a
suspended integration up front.

```text
$ patchy backfill gh --repo acme/
Backfill request backfill-x7k2p created on integration gh.
Watch it with:
  patchy get backfillrequests backfill-x7k2p
  kubectl get backfillrequests backfill-x7k2p
```

`patchy describe backfillrequest <name>` shows the phase (`Pending`, then `Complete` or `Failed`), the listed and
ingested counts, and the last error. Watch for `truncated`: it means the page budget ran out, and you need a request
with a narrower `--repo` prefix to reach the rest. Finished requests expire on a TTL.

### Asking for a backfill

A request that runs at once needs the `backfill` verb on integrations. Without it, pass `--request`. The request then
waits in `AwaitingApproval` until an operator decides it, and filing one needs only `create` on backfillrequests:

```text
$ patchy backfill gh --repo acme/shop --request
Backfill request backfill-m2bns filed on integration gh. It waits in AwaitingApproval
until an operator approves it:
  patchy approve backfillrequest backfill-m2bns
Watch it with:
  patchy get backfillrequests backfill-m2bns
  kubectl get backfillrequests backfill-m2bns
```

Leaving out `--request` without the verb fails before anything is written, and the error suggests `--request`.

### Deciding a request

An operator holding the `backfill` verb on integrations approves or denies a waiting request. The decision lands on the
request's `spec.decision` with your identity and the time, and it is final:

```sh
patchy get backfillrequests                                     # what is waiting (phase AwaitingApproval)
patchy describe bfr backfill-m2bns                              # who asked, for which repositories
patchy approve backfillrequest backfill-m2bns [--note "go ahead"]
patchy deny    backfillrequest backfill-m2bns --reason "too broad; ask for acme/shop"
```

`--note` on approve and `--reason` on deny are shown to the requester. Repeating the same verdict succeeds and changes
nothing. Contradicting an earlier decision, or deciding a request that was not filed with `--request`, fails with exit
code 4. Both verbs accept several names, and `--dry-run` reports without writing.

## Configuration

The cluster-free command groups (`dev`, `mirror` and `build`) read one layered patchy configuration file. Its locations,
merge rules and shape are documented in [Configuration](../supply-chain/configuration.md). The cluster verbs above read
only your kubeconfig.

## Testing a generic integration

Like `mirror` and `build`, the `dev` commands never touch a cluster. They are a local test harness for authors of
[generic integrations](../integrations/sources/generic.md).

```sh
patchy dev generic --secret dev-secret --enhance-url http://127.0.0.1:9000/enhance
```

This command hosts the real generic webhook receiver on your workstation. It runs the same HMAC authentication,
deduplication, and validation as the integration-controller. It keeps ingested findings in memory, and it drives the
enhancer call and the resolver write-back against your endpoints. So one signed POST tests every exchange in the
contract. For a process that is only an enhancer or only a resolver, `patchy dev enhance` / `patchy dev resolve` fire a
single signed exchange from a findings payload file, with no server involved.

You can also set every `dev` flag from `PATCHY_DEV_*` environment variables and the `dev:` block of the
[patchy configuration](#configuration). A flag beats the environment, and the environment beats the file. See the
[generic integration guide](../integrations/sources/generic.md#testing-your-integration-locally) for the full
walkthrough.

## Mirroring and building artifacts

Two command groups publish the software a platform runs as signed OCI artifacts in your own registries. They share the
config's `registries`, `signing` and `scan` blocks. Neither touches a cluster or runs git. The
[supply chain guide](../supply-chain/index.md) covers both end to end, with CI recipes.

**`patchy mirror`** maintains a vendored mirror store. This is a git tree in which every `charts/<name>/` or
`artifacts/<name>/` directory pins one upstream helm chart or OCI artifact. Each entry is vendored (copied into the
tree) for review, locked to a digest, checked against its publisher's signatures, scanned, and published signed. See
[Mirroring charts and images](../supply-chain/mirror.md).

```sh
patchy mirror add dex --url https://charts.dexidp.io   # scaffold an entry (and a starter patchy.yaml)
patchy mirror upgrade --check -o json                  # what would move, as data
patchy mirror upgrade --all                            # move pins, regenerate everything derived
patchy mirror validate --all                           # the CI gate: current, verified, clean
patchy mirror sync --all                               # converge every registry, idempotently
```

**`patchy build`** builds the Dockerfiles and Flux manifest directories listed under `build:`, scans them before
anything is pushed, publishes every tag (tags never move), and signs. `patchy build promote` adds a tag to the newest
signed build matching a pattern, without rebuilding. See [Building images and manifests](../supply-chain/build.md).

```sh
patchy build --all --dry-run              # PR CI: build and scan, never contact a registry
patchy build --all -o markdown            # publish and sign, summary ready for a job summary
patchy build --all --update-allowlists    # regenerate security/allowlists/<name>.yaml
patchy build promote --all --from '${VERSION}-edge.*' --tag 'v${VERSION}'   # release what CI already built
```

Like `dev`, the `mirror` flags can also be set from `PATCHY_MIRROR_*` environment variables and the `mirror:` block.
Both groups run the `cosign` binary (v3 or later) for signing and verification. They also run whichever scanners the
config enables (`osv-scanner`, `grype`, `kubescape`). `build` also needs `docker` with buildx for image entries.

## Permissions

Each action is a **custom RBAC verb**, granted separately, so holding `approve` does not grant `suspend`. The
per-finding verbs (`approve`, `retry`, `expedite`, `suspend`, `resume`) are on `findings.patchy.bitwisemedia.uk`. The
integration-scoped ones (`backfill`, `replay`, `reset`) are on `integrations.patchy.bitwisemedia.uk`. Deciding a
backfill request uses `backfill` too. To see yours:

```sh
patchy can-i                # the full matrix, findings and integrations
patchy can-i approve        # one verb; exit code answers, for shell conditionals
patchy can-i backfill       # integration-scoped verbs resolve on integrations
```

Two things check those verbs, but only one of them enforces them:

- The CLI runs a `SelfSubjectAccessReview` before writing. This is for **convenience**: you get a fast, clear failure
  that names the verb you lack, instead of an unclear rejection from the server. It provides no security, and anyone can
  bypass it by not using the CLI.
- A **`ValidatingAdmissionPolicy`** in the cluster binds each spec field to its verb. It runs inside the API server's
  admission chain, so it applies the same way to `patchy`, `kubectl edit`, `kubectl patch`, server-side apply and raw
  `curl`. This is the actual enforcement. There are three policies. One covers findings. One covers integrations: it
  gates `spec.replay`/`spec.reset` and leaves the rest of the operator's configuration free to update. The third covers
  backfill requests: `spec.requestedBy` must be your own username, a request that runs at once needs `backfill`, and so
  does setting `spec.decision`.

The policy is needed because Kubernetes RBAC has no notion of a field: `update` on findings allows changes to every
field of the object. Without the policy, letting a developer suspend a finding would also let them rewrite its severity
or forge an approval. See [Kustomize](../deployment/kustomize.md) for the manifest and the role ladder.

!!! warning "Requires Kubernetes 1.30+"

    `ValidatingAdmissionPolicy` reached GA in 1.30. On an older cluster the policy will not install, and
    enforcement silently falls back to plain RBAC, where anyone with `update` can change every field. Check with
    `kubectl api-resources | grep validatingadmissionpolic`.

The policy lists the spec fields it protects. So a contributor who adds a field to `FindingSpec` must extend it. See
[Adding a field to FindingSpec](../contributing/finding-fields.md).

## Exit codes

| Code | Meaning                                                                    |
| ---- | -------------------------------------------------------------------------- |
| `0`  | success                                                                    |
| `1`  | runtime failure (unreachable cluster, bad response)                        |
| `2`  | usage error                                                                |
| `3`  | the named resource does not exist                                          |
| `4`  | RBAC refused, or the action is unavailable in the resource's current state |

## A triage session

```sh
patchy get findings --awaiting -o wide      # what needs a human
patchy review finding <name>                # what the agent concluded, and why
patchy browse finding <name>                # the tracking issue, if you want the thread
patchy approve finding <name>               # release the hold
```

## Output and piping

Rendered reports use the dark theme by default. On a light terminal, set the same variable that `glow` and the other
charm tools read:

```sh
export GLAMOUR_STYLE=light     # or dark, dracula, tokyo-night, notty, ascii
```

There is no automatic detection. Finding a terminal's background colour needs a query and response round trip, which
only an interactive event loop can handle, and `patchy` is a one-shot command. Tables are not affected. They use the
ANSI 0-15 palette, so they inherit your terminal's theme.

`stdout` carries data and `stderr` carries messages. So `-v` never corrupts a pipe, and "no findings found" goes to
stderr instead of into your `jq`. Styling turns itself off whenever stdout is not a terminal. It also honours
`--no-color`, `NO_COLOR`, and `TERM=dumb`.

```sh
patchy get findings -o json | jq '.items[] | select(.status.priority == "critical") | .metadata.name'
patchy get findings -o name | xargs -n1 patchy describe finding
```
