# Remote evaluation

Patchy can also run [evolve](https://github.com/bitwise-media-group/evolve) skill evaluations remotely, in the same
sandboxed agent pods it uses for findings. This is optional: the evaluation-controller is off by default in the Helm
chart, and nothing in the finding pipeline depends on it.

The two sides split the work strictly. Evolve owns everything about what an evaluation means: the specs, the grading,
the LLM judge and the baselines. Patchy owns scheduling, sandboxing and state. It never learns what an evaluation means.

## What evolve submits

An evolve client logs in with OIDC, uploads its workspace as a bundle named by its content hash, and submits a plan of
units. Each unit names a skill, a tier, a model, an ordered list of acceptable harnesses and the workspace digest. The
[evaluation-controller](../configuration/evaluation-controller.md) validates the submission and records it as an
`Evaluation` resource.

## The two kinds

| Kind             | Short name | What it is                                                                                   |
| ---------------- | ---------- | -------------------------------------------------------------------------------------------- |
| `Evaluation`     | `eval`     | One submission. Phase `Pending → Running → Complete` or `Failed`, derived from its children  |
| `EvaluationUnit` | `evu`      | One unit of the plan, run as one agent Job. Phase `Pending → Running → Complete` or `Failed` |

The controller expands each `Evaluation` into its `EvaluationUnit` children (condition `UnitsCreated`). It then
schedules the units on the same agent-Job machinery as investigations, with a limit on how many run at once. Each pod
runs `evolve exec-unit` against the downloaded bundle and emits an `EVOLVE-EVENT:` JSONL stream on stdout.

Patchy reads only the `result` and `fatal` events. A short summary of fixed size (cases passed, failed and errored,
usage, timing) goes on the unit's status. Patchy does not read the full results entry. It stores the entry in a per-unit
ConfigMap, and the client puts the results back together locally. A unit that cannot run ends as `Failed` with a reason:
`WorkspaceLost`, `HarnessUnavailable`, `JobFailed`, `Aborted` or `ResultTooLarge`.

These phases are local to the evaluation kinds. They are never part of the [Finding lifecycle](lifecycle.md), and the
evaluation-controller is their only writer. Finished evaluations expire on a TTL (72 hours by default).

## Isolation

Evaluation pods follow the same [isolation model](isolation.md) as finding pods. Brokered claude and codex pods hold no
model credential. Their traffic goes through the [egress broker](../configuration/egress-broker.md). The workspace
bundle arrives through source-controller's artifact endpoint, addressed by its digest.

## The wire contract

`pkg/evaluation` is the public, stdlib-only Go package that defines the contract between patchy and evolve: the HTTP
submission types, the in-pod `EVOLVE-EVENT:` stream, and the SSE monitor stream the API relays to the client. Evolve
imports it directly.

Access is native Kubernetes RBAC on the `evaluations` resource: `create` to submit and upload, `get` to read and
monitor, `delete` to cancel. See [evaluation-controller](../configuration/evaluation-controller.md) for the flags, the
authentication config and the routes.
