# Models and credentials

This page is the single reference for three things the controllers, the agent pods and the Helm chart all share: how
model ids are written, how the remediation budget works, and where each provider's model credential lives.

## Model ids and aliases

Every model setting (`--model-allowlist`, `--investigate-model`, `--remediate-model`, and the matching
`agent.modelAllowlist`, `agent.investigate.model` and `agent.remediate.model` chart values) accepts an id in one of
three forms. The controllers resolve it once, at startup:

| Form          | Example                       | Resolves to                                                                        |
| ------------- | ----------------------------- | ---------------------------------------------------------------------------------- |
| Registered id | `anthropic/claude-sonnet-5-5` | That model, exactly                                                                |
| Family alias  | `anthropic/claude-sonnet`     | The newest version of that family patchy registers (today Sonnet 5.5 and Opus 5.5) |
| Pattern id    | `anthropic/claude-opus-6`     | An Anthropic version patchy has not registered yet: claude harness only, unpriced  |

The defaults are aliases: `anthropic/claude-sonnet` for both stages, and `anthropic/claude-sonnet,anthropic/claude-opus`
for the allowlist. An upgrade of patchy that registers a newer model therefore moves them with no configuration change.
Pin a concrete id to hold a version.

Only the **concrete** id flows downstream. The pod environment, the investigation prompt's model list, CRD status and
the rollups all record a version, never an alias, so statistics and pricing stay per version.

A pattern id lets a new Anthropic release run before patchy ships an entry for it. It must be well formed
(`anthropic/claude-<family>-<major>[-<minor>]`), runs on the claude harness only, and has no token pricing until it is
registered. The claude CLI reports its own cost, so run costs are still recorded. The controller logs a warning naming
every such id at startup. An alias whose family patchy does not know, or an id of neither shape, is a startup error.

The **harness** (the coding-agent CLI: `claude -p` or `codex exec`) is derived from the model's provider, never
configured beside it. Anthropic ids run on claude, OpenAI ids on codex.

## The remediation budget

The investigation stage runs on fixed limits: `--investigate-max-turns` (25), `--investigate-token-budget` (150,000) and
`--investigate-timeout` (15 minutes). Remediation is different, because the investigation report **estimates** how many
turns and output tokens the fix will need. The estimate never shrinks a run; it only decides whether a human must
approve it first.

The budget has two levels, set on the remediation-controller:

| Budget     | Flags                                                             | Defaults        | Meaning                                                                            |
| ---------- | ----------------------------------------------------------------- | --------------- | ---------------------------------------------------------------------------------- |
| **Auto**   | `--remediate-auto-max-turns`, `--remediate-auto-token-budget`     | 80 / 400,000    | What every unattended fix gets, and the line past which an estimate needs approval |
| **Manual** | `--remediate-manual-max-turns`, `--remediate-manual-token-budget` | 240 / 1,200,000 | The most a single human approval can grant                                         |

How a run's budget is decided:

1. Every remediation runs on at least the auto budget, whatever the investigation estimated, and including when it
   estimated nothing.
2. An estimate above the auto budget holds the finding in `AwaitingApproval` instead of queueing it (the
   `exceedsAutomatedTurns` or `exceedsAutomatedTokens` hold).
3. Approving that finding grants the estimate, capped at the manual budget.

Beyond the approval gate, the estimate is reporting only. Each run records the estimate, the granted budget and the
actual usage, and the rollups average the skew. That average feeds back into the next investigation's prompt
(`PATCHY_CALIBRATION`) so later estimates can correct for it.

Each flag is also a `PATCHY_REMEDIATE_{AUTO,MANUAL}_{MAX_TURNS,TOKEN_BUDGET}` environment variable. The
investigation-controller takes the same four flags, but only to tell the analysis prompt what an estimate will buy
unattended and what an approval can grant. They are never a clamp there, and they must match the
remediation-controller's values. In the chart, both controllers read one block:

```yaml
agent:
  remediate:
    model: anthropic/claude-sonnet # fallback when the report's suggestion is missing or off the allowlist
    timeout: 45m
    auto: { maxTurns: 80, tokenBudget: 400000 }
    manual: { maxTurns: 240, tokenBudget: 1200000 }
```

The manual budget must be at least the auto budget: approving a fix must never buy less than leaving it alone. The agent
runner refuses to start when a `MANUAL` value is below its `AUTO` value. Token budgets are enforced live: the runner
watches the harness's streamed usage and kills the process group once the cumulative output-token count passes the
budget.

## Provider credentials

Agent pods hold **no** model credential. Every claude and codex request goes through the
[egress broker](../configuration/egress-broker.md), which authenticates the calling pod by its projected ServiceAccount
token, strips it, and attaches the real credential on the way out. The model credential therefore lives with the broker,
in the **release** namespace, never in the agent namespace. The broker deploys whenever a claude or codex runner is
enabled.

| Runner | Provider  | Credential                                                         | Chart values                                                                                       |
| ------ | --------- | ------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------- |
| claude | Anthropic | API key, or a `claude setup-token` OAuth token sent as a bearer    | `egressBroker.anthropicSecret` (key `api-key`), `egressBroker.anthropicAuth: key \| token`         |
| claude | Bedrock   | The broker's AWS identity (IRSA or EKS Pod Identity), SigV4-signed | `agent.runners.claude.provider: {name: bedrock, region}` + SA annotation                           |
| claude | Vertex AI | The broker's Google identity (GKE Workload Identity), OAuth bearer | `agent.runners.claude.provider: {name: vertex, region, projectID}` + SA annotation                 |
| claude | Foundry   | An API key, or Entra through Azure Workload Identity               | `agent.runners.claude.provider: {name: foundry, resource, modelMap}`, `egressBroker.foundrySecret` |
| codex  | OpenAI    | A platform API key, sent as a bearer                               | `egressBroker.openaiSecret`                                                                        |
| codex  | Azure     | An `api-key`, or Entra through Azure Workload Identity             | `agent.runners.codex.provider: {name: azure, resource, modelMap}`, `egressBroker.azureSecret`      |

```yaml
# First-party Anthropic (the default).
egressBroker:
  anthropicSecret: patchy-anthropic # key: api-key

# Amazon Bedrock: SigV4-signed with the broker's AWS identity (IRSA here).
agent:
  runners:
    claude:
      provider:
        name: bedrock
        region: us-east-1 # model ids derive as us.anthropic.<model>
egressBroker:
  serviceAccount:
    annotations:
      eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/patchy-broker

# Google Vertex AI: OAuth through GKE Workload Identity.
agent:
  runners:
    claude:
      provider:
        name: vertex
        region: europe-west1
        projectID: my-project
egressBroker:
  serviceAccount:
    annotations:
      iam.gke.io/gcp-service-account: patchy-broker@my-project.iam.gserviceaccount.com

# Microsoft Foundry: deployment names cannot be derived, so the model map is
# required. Key it by CONCRETE ids: an alias in the allowlist
# (anthropic/claude-sonnet) is checked under the version it resolves to.
agent:
  runners:
    claude:
      provider:
        name: foundry
        resource: my-foundry
        modelMap:
          anthropic/claude-sonnet-5-5: my-sonnet-deployment
          anthropic/claude-opus-5-5: my-opus-deployment
egressBroker:
  foundrySecret: patchy-foundry # omit for Entra through Azure Workload Identity

# Codex on OpenAI.
agent:
  runners:
    codex:
      enabled: true
egressBroker:
  openaiSecret: patchy-openai # key: api-key
```

Bedrock and Vertex model ids are derived for every registered model, and for pattern ids too. Foundry and Azure
deployment names cannot be derived, so their `modelMap` must cover every allowlisted model and stage default that
resolves to that runner; the controllers refuse to start otherwise. Scope cloud roles to invoking models only:
`bedrock:InvokeModel*`, `aiplatform.endpoints.predict`, or Cognitive Services User. See
[egress-broker](../configuration/egress-broker.md#workload-identity) for the workload-identity recipes.

A ChatGPT-plan codex token (`CODEX_ACCESS_TOKEN`) cannot be brokered. Use an OpenAI platform API key, or Azure OpenAI.

!!! note "Migrating from a release before the broker"

    The model credential used to live in the **agent** namespace and was wired into the pod. Now the broker is its only
    consumer, and it lives in the **release** namespace:

    1. Create `patchy-anthropic` (and `patchy-openai` for codex) in the release namespace.
    2. Upgrade the chart. The broker deploys.
    3. Delete the old Secrets from the agent namespace.

    The `agent.runners.<harness>.secret*`, `hosts` and `dnsPatterns` values are ignored, and a NOTES warning fires while
    they are still set. If the old claude `secretEnv` was `CLAUDE_CODE_OAUTH_TOKEN`, set `egressBroker.anthropicAuth:
    token` so the broker sends the OAuth token as a bearer.
