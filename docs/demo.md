---
title: Demo script
template: demo.html
hide:
  - navigation
---

# Patchy demo script

<div class="pt-demo-lede" markdown>

**10–15 minutes.** Findings arrive → Patchy triage → verdicts (false positive / remediate / manual) → human review.

Timing assumes findings are already in flight or can be **Expedite**d. Call out any wait out loud.

_Last verified against v0.13._

</div>

!!! warning "Say early, say once more at the close"

    **What we show live is GitHub Advanced Security / CodeQL.** It is one of several sources that ship today, alongside
    Google Cloud Security Command Center, Wiz and the generic HTTP integration for your own scanners. Ingestion is
    pluggable (`pkg/source`). The rest of the pipeline works the same for every source.

---

## Prep {#prep}

<div class="pt-demo-section-label" markdown>

Not on camera · run through before the room

</div>

| Ready? | Item                                                                                                | Why                              |
| :----: | --------------------------------------------------------------------------------------------------- | -------------------------------- |
|   ☐    | Demo GitHub repo with CodeQL + intentional findings (FP, auto-fixable real, optional “needs human”) | Covers all three recommendations |
|   ☐    | GitHub App / Integration + Forge already live                                                       | Webhooks land without live setup |
|   ☐    | Patchy status page open (signed in)                                                                 | Second surface alongside GitHub  |
|   ☐    | Short accumulation + min-age **or** **Expedite** on new findings                                    | Avoid a 1h+ wait mid-demo        |
|   ☐    | Optional safety net: one finding at `Investigating`, one `Dismissed`, one with open PR              | If the live path is slow         |

!!! abstract "One-line product framing · use once"

    Patchy is an end-to-end pipeline for triaging and remediating security findings **from multiple sources**.
    Kubernetes custom resources are the state machine. GitHub issues are a one-way view for humans. Findings
    accumulate, get context, and get investigated by a sandboxed agent. High-confidence fixes become pull requests
    for humans to merge. **CodeQL via GitHub Advanced Security is the first integrated source and the path in this
    demo. It is not the limit of the product.**

---

## Act 0: Frame the problem {#act-0}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">0:00–1:30</span> <span class="pt-demo-dur">~1.5 min</span>

</div>

!!! example "On screen"

    Patchy status **Findings** board (or a slide with the phase rail).

!!! quote "Talk track"

    Static analysis is cheap to run and expensive to triage. Scanners find real problems _and_ a lot of noise. Most
    teams either ignore the backlog or spend senior engineers’ time on every alert.

    Patchy is an end-to-end pipeline for **triaging and remediating security findings from multiple sources**.
    Kubernetes custom resources are the state machine. GitHub issues are a one-way view for humans. Findings
    accumulate, get context, and get investigated by a sandboxed agent. High-confidence fixes become pull requests
    for humans to merge.

    **What we show today is GitHub Advanced Security / CodeQL**: `code_scanning_alert` webhooks into the
    integration-controller. **It is not the only source that ships.** Google Cloud SCC, Wiz and a generic HTTP
    integration come in the same way. Ingestion goes through a **plugin interface** (`pkg/source`). Each source turns
    its alerts into the same `Finding` model: advisories, severity, locations and repository identity. Everything
    after that is **source-agnostic**: accumulation, the tracking issue, context enhancement, investigation, verdict
    routing and remediation. To add another scanner or platform, you write a handler and wire up its deliveries. You
    don’t rewrite the pipeline.

    On every tracking issue you’ll see `security-source: …` (today often `ghas`). That label tells people and
    searches _which_ origin produced the finding. After ingest, the pipeline treats every source the same.

!!! info "Phase rail · point once"

    <code class="pt-demo-rail-phases">Opened → Enhanced → Investigating → Queued → Remediating → InReview → Remediated</code>

    Plus the end states and holds: `Dismissed` · `HandedOff` · `AwaitingApproval` · `Failed`

!!! tip "Transition"

    “For this demo we’ll drive CodeQL. But keep in mind: any source that speaks the Finding contract gets the same
    lifecycle.”

---

## Act 1: Push → CodeQL → dual visibility {#act-1}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">1:30–4:00</span> <span class="pt-demo-dur">~2.5 min</span>

</div>

### 1a. Push · ≈30s

!!! example "On screen"

    Demo repo → commit/push (or open a PR that will run analysis).

!!! quote "Talk track"

    We start where developers already work: a normal push to GitHub. No special CLI, no agent on the laptop. For
    **this** source, the GitHub App’s single webhook URL points at Patchy’s **integration-controller**. That is the
    internet-facing entry point for GitHub deliveries. Other sources come in through the same controller, each with
    its own validated webhook and integration config. There is one front door, with a handler for each source
    behind it.

### 1b. CodeQL pipeline · ≈45s

!!! example "On screen"

    Actions tab → CodeQL / code-scanning workflow running or completed.

!!! quote "Talk track"

    CodeQL analyzes the code and opens or updates **code scanning alerts**. For **this** path, GitHub Advanced
    Security stays the scanner of record. Patchy does not replace CodeQL, or any other scanner. It **consumes**
    findings. Here those are `code_scanning_alert` deliveries. The GHAS source handler normalizes them (rule,
    severity, locations, CWE/CVE/GHSA) and folds them into Findings.

    A future source could be another SAST vendor, container scanning, dependency advisories or an internal scanner.
    Its raw payloads would look different. It would still produce the same `Finding` resource and move through the
    same phases you’re about to watch.

### 1c. GitHub Security → Code scanning · ≈30s

!!! example "On screen"

    **Security → Code scanning** on the repo; open one alert.

!!! quote "Talk track"

    These are the same alerts developers and AppSec already know from **GHAS**. Other sources may show findings in
    their own product UIs. Patchy’s job is the **shared triage and remediation layer** on top: one board, one
    priority model and one investigation report format, whatever the origin.

### 1d. Patchy status page · ≈45s

!!! example "On screen"

    Status page **Findings** board; filter to the demo repo if needed.

!!! quote "Talk track"

    Within moments of the webhook, the same work shows up here: severity, phase, confidence (once investigated),
    verdict and age. The page updates live over Server-Sent Events, so nobody has to hit refresh.

    Click into a finding: Overview, Alerts, Timeline, Investigation, Remediation. The sidebar links the tracking
    issue, owners, advisories and **source**. This board is the **multi-source pipeline view**, not a CodeQL screen.
    Today every row may say GHAS. Connect SCC or Wiz and the same columns mix origins, with the same flow.

??? note "Optional · kubectl visible"

    `kubectl get findings -w` shows the same state machine without the UI: phase, severity, priority and verdict.
    The source is on the CR and in the `security-source` labels.

!!! tip "Demo tip · accumulation"

    Several alerts from the same advisory family against the same repo **accumulate for about 1 hour** into one
    Finding. Accumulation is a condition (`AccumulationComplete`), not a phase. Say so if the board shows alert counts
    growing while the phase stays put. For live demos, **Expedite** skips the accumulation window and the minimum
    age, so the gate can admit the finding right away.

---

## Act 2: Context enhancement {#act-2}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">4:00–5:30</span> <span class="pt-demo-dur">~1.5 min</span>

</div>

!!! example "On screen"

    Finding moving `Opened → Enhanced` (status page phase or tracking-issue comment).

!!! quote "Talk track"

    Before any agent reads code, the **context-controller** runs a chain of enhancers. They record ownership and
    infrastructure relationships as enrichments on the Finding. The tracking issue shows them as sticky comments and
    labels.

    Patchy ships lookups for Google Cloud, AWS and Azure resources, a generic HTTP enhancer for your own systems, and
    a YAML file that stands in for a CMDB. A real CMDB plugs in through the generic enhancer or the `pkg/enhance`
    plugin interface. One important point: enhancement holds **no GitHub credential**. It only writes Finding status.
    Writing to the tracking issue stays with the integration-controller.

    This stage is **source-agnostic**: every Finding gets the same ownership context, whatever its origin. So when the
    investigation report lands, people already know _who owns this_ and _where it lives_, as well as _what the
    scanner said_.

!!! success "Beat"

    The phase pill flips to **Enhanced**. If you open the issue, the enrichment comment is there.

---

## Act 3: Investigation and the three ratings {#act-3}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">5:30–8:30</span> <span class="pt-demo-dur">~3 min</span>

</div>

!!! example "On screen"

    Phase → **Investigating**; optional agent Job logs (`PATCHY-EVENT:` stream) or finding detail Timeline.

!!! quote "Talk track"

    Once accumulation closes and the finding is old enough (default **one hour**, our patience setting), the
    **investigation-controller** admits it. First it creates the `Repository`. The **source-controller** pins that
    repository to one SHA and serves it as a tarball. Then the investigation-controller creates an immutable
    `Investigation` and schedules a sandboxed agent Job.

    Isolation in short: the agent pod has **no credential of any kind**, no Kubernetes API access, and no open
    internet access. All it gets is a digest-verified tarball of the repo. Its model traffic is authenticated at the
    egress broker. Investigation and remediation see the **same pinned tree**.

    Investigation does not care whether the finding came from CodeQL or another handler. The agent gets a finding
    handoff built from a template, plus a SHA-pinned tree. Ratings, recommendation and confidence follow the same
    contract for every source.

### The three ratings · must cover

!!! quote "Talk track"

    The agent does more than say “true” or “false.” It rates three dimensions, each
    `none | low | medium | high | critical`, with a short justification:

| Dimension          | Question the agent answers                                                                       |
| ------------------ | ------------------------------------------------------------------------------------------------ |
| **Exploitability** | Can this actually be exercised _in this codebase_? Reachable entry points, or why none exist.    |
| **Likelihood**     | How probable is exploitation _in this deployment_? Exposure, preconditions, known exploit paths. |
| **Impact**         | What’s the blast radius if it fires? Data, privilege, lateral movement, availability.            |

    These ratings feed a **0–100 scheduling priority** when work queues for remediation:

    **severity 30% · exploitability 30% · likelihood 20% · impact 20%** (tunable).

    Scanner severity alone is not enough. A finding the scanner calls “high” but that nothing can reach should not
    take slots from a medium one on a live, internet-facing path. Inflated ratings take capacity away from urgent
    work, and the prompt tells the agent so.

### Confidence scoring · must cover

!!! quote "Talk track"

    Separately, the agent sets **`confidence` ∈ [0.0, 1.0]**:

    - Probability that the **recommendation is correct**.
    - For **`remediate`**, specifically: probability the finding can be **fully fixed without breaking functionality**.

    That number is the gate for automated remediation. The default threshold is **0.75**:

    | Confidence path | What happens |
    | --------------- | ------------ |
    | `remediate` **≥ 0.75** and no breaking-change hold | **Queued** automatically |
    | `remediate` **&lt; 0.75** _or_ better-but-breaking alternative | **AwaitingApproval** until `/approve` (issue or status page) |

    Backwards-compatible fixes always come first. If a cleaner fix would break callers, the report can still
    recommend the safe fix, set `breaking_change_available: true`, and describe the better option for a human.

### The three recommendations

| Recommendation  | Meaning                                                                      | Terminal / next phase                                                 |
| --------------- | ---------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| **`ignore`**    | False positive (not exploitable, bad dataflow assumption, already mitigated) | **Dismissed**: source alerts dismissed where applicable, issue closed |
| **`remediate`** | Real issue; automated fix likely to succeed                                  | **Queued** / **AwaitingApproval** / then agent fix                    |
| **`manual`**    | Real issue; needs domain judgment or is too risky to automate                | **HandedOff** to owners (revivable via `/approve`)                    |

!!! tip "Transition"

    “We’ll walk each path. Start with the cheap win: false positives.”

---

## Act 4: False positive early exit and token cost {#act-4}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">8:30–10:30</span> <span class="pt-demo-dur">~2 min</span>

</div>

!!! example "On screen"

    Finding with recommendation **ignore** → phase **Dismissed**; GitHub code-scanning alert dismissed as false
    positive (for the GHAS path); the Metrics view if useful.

!!! quote "Talk track"

    Sometimes the agent can _prove_ the finding is not real: a dead path, a sanitizer already in place, or the tool
    misreading the dataflow. Then it recommends **`ignore`**. The controllers dismiss the accumulated alerts as false
    positives (for GHAS, the Code scanning alerts) and close the tracking issue. The phase lands at **Dismissed**.
    No remediation Job, no PR noise, and no backlog entry that nobody will ever fix.

    **This is the economic early exit.** Investigation runs on much smaller budgets than remediation:

| Stage       | Default budgets (order of magnitude)                             |
| ----------- | ---------------------------------------------------------------- |
| Investigate | ~**25** turns · **~150k** token budget · ~15m timeout            |
| Remediate   | **80** turns · **~400k** tokens unattended (auto) · ~45m timeout |

    The investigation report also suggests a model and _estimates_ the turns and tokens the fix needs. The model
    must be on an allowlist. An estimate above the auto budget waits for a human approval, which can grant up to the
    manual budget (240 turns, 1.2M tokens by default). A false positive never starts that second stage.

    The **Metrics** view works well on a wallboard. It shows the outcome mix, per-stage **token / cost / duration**
    and the verdict mix, and you can filter it to one repository, model or harness. Point at the `ignore` share, and
    compare investigation-stage cost with remediation-stage cost:

    > Every false positive we stop at investigation is a remediation run we never pay for. It’s also a PR that
    > reviewers never have to switch context for.

    The same accounting is also stored on the CRs (`Investigations`, `FindingRollups`). The Metrics view is a
    friendlier way to read it.

??? note "Optional beat"

    Open the investigation report on the issue. Exploitability is often `none`/`low`, with a clear summary of why the
    code is not reachable. That justification is what AppSec would otherwise write by hand.

---

## Act 5: Remediation, where agents open PRs {#act-5}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">10:30–13:00</span> <span class="pt-demo-dur">~2.5 min</span>

</div>

!!! example "On screen"

    Finding with **`remediate`**, confidence ≥ 0.75 → **Queued → Remediating → InReview**; open the PR.

!!! quote "Talk track"

    A real finding that can be fixed automatically, with confidence above the bar, enters the remediation queue. The
    **remediation-controller** schedules work in **priority order**, with a cap on how many runs go at once. Aging
    makes sure low-priority work doesn’t wait forever.

    A second sandboxed agent gets the finding markdown, the **same** pinned tree and the investigation report, under
    the clamped budget. It must produce a summary report **and** a `commit.sh` that actually produces commits. The
    controller **checks that claim**. It doesn’t take the model’s word for it.

    The agent still has **no write credential**. Only the remediation-controller holds a short-lived, scoped forge
    write token. It replays the changeset through the GitHub API onto a branch named **`patchy/<finding>`**,
    **opens the pull request**, and moves the finding to **InReview**.

    Humans stay in the merge path. Merging fires a webhook → **Remediated**. Closing the PR without merging →
    **Failed** (you can retry it from the status page).

!!! example "On screen · PR body"

    Review this like any other PR: diff, CI, ownership. Patchy automated the _investigation_ and the _proposal_.
    Merging is still a human decision. The machine generates the fix, and a person decides whether to accept it.

??? warning "If confidence is low or breaking-change hold"

    Show **AwaitingApproval**, then click **Approve** on the status page (or comment `/approve` on the issue). The
    finding takes the same transition into **Queued**. The status server never moves phases itself. It records the
    approval, and the controller makes the transition.

---

## Act 6: Manual path and close {#act-6}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">13:00–14:30</span> <span class="pt-demo-dur">~1.5 min</span>

</div>

!!! example "On screen"

    Finding with recommendation **`manual`** → **HandedOff** (if available); otherwise narrate.

!!! quote "Talk track"

    Not everything should be fixed automatically: auth redesigns, unclear product risk, work that spans several
    services. **`manual`** hands the finding to the repository owners through the tracking issue (**HandedOff**),
    with the full investigation report as context. A later `/approve` can still bring it back into the queue if
    someone decides automation should try.

    People can also close the tracking issue at any non-terminal phase, which moves the finding to **HandedOff**. A
    human can always pull work out of the pipeline.

!!! success "Wrap · 30–45s"

    End to end for this demo: **push → CodeQL → alerts in GitHub Security and on Patchy’s status page → context →
    investigation with three ratings and a confidence score → early-exit dismissals that save remediation tokens →
    high-confidence remediations that open real PRs → humans merge.**

    The **scanner stays the scanner**. Today that’s CodeQL via GitHub Advanced Security. Tomorrow it’s whatever you
    plug into the source plugin interface. Patchy is the **extensible triage and safe auto-remediation layer** on
    top. It takes findings from many sources, keeps its own state, works in priority order, keeps credentials away
    from the agents, and reports what it did on the Metrics view.

---

## Timing card {#timing-card}

<div class="pt-demo-section-label" markdown>

Presenter cheat sheet · glance while talking

</div>

|       Clock | Act                                  | Key visual                  | Callout                      |
| ----------: | ------------------------------------ | --------------------------- | ---------------------------- |
|   0:00–1:30 | Problem + **multi-source framing**   | Status board                | GHAS first; plugin interface |
|   1:30–4:00 | Push → CodeQL → dual surface         | Actions + Security + status | “This path today”            |
|   4:00–5:30 | Context enhancement                  | `Opened → Enhanced`         | Source-agnostic stage        |
|   5:30–8:30 | Investigation, 3 ratings, confidence | Investigating               | Same report for any source   |
|  8:30–10:30 | False positive early exit + cost     | Dismissed + Metrics         | Economic early exit          |
| 10:30–13:00 | Remediation opens PRs                | PR `patchy/<finding>`       | Verify-don’t-trust           |
| 13:00–14:30 | Manual + wrap                        | HandedOff / summary         | Restate extensibility        |

<div class="pt-demo-flex" markdown>

!!! tip "Running short"

    Cut Act 6 detail. Keep the multi-source framing, the three ratings, the confidence threshold (0.75), the FP cost
    contrast, and the PR opening.

!!! tip "Running long"

    Briefly show the Approve path, Expedite, or `kubectl get findings -w`.

</div>

---

## Suggested live path

1. **False positive first.** It proves the early exit and the cost story. Remediation can run in the background
   meanwhile if needed.
2. **High-confidence remediate.** Open the PR while you talk about isolation and verify-don’t-trust.
3. **Manual or AwaitingApproval.** Shows that humans stay in control.

---

## Audience Q&A {#qa}

<div class="pt-demo-section-label" markdown>

Optional · have ready, don’t force

</div>

??? question "Does Patchy replace CodeQL?"

    No. CodeQL/GHAS is one integrated source, and Patchy is not limited to it. SCC, Wiz and a generic HTTP integration
    ship too. All of them feed a **source-agnostic** `Finding` model through a **plugin interface** (`pkg/source`).
    New scanners are added as handlers, without reworking accumulation, investigation or remediation.

??? question "What does multi-source look like on the board?"

    Same phases and verdicts. The origin is shown in `security-source` (and in the finding metadata). People filter by
    source when they care. After ingest, the pipeline doesn’t split by vendor.

??? question "What if a source has no GitHub alerts UI?"

    The finding still shows up on the tracking issue and the status page. Remediation needs a Forge configured for
    that repository, which provides the read and write access. A finding without a remediable forge can still be
    triaged (investigated, dismissed or handed off), even if it never reaches the PR stage.

??? question "What if the agent is wrong?"

    The confidence gate, AwaitingApproval and human merge on PRs all catch it. Dismissals can be revived too (reopen
    the issue → HandedOff).

??? question "Where’s the state?"

    Only in Kubernetes CRs. Issues and labels are never read back as the source of truth.

??? question "How do we see spend?"

    The Metrics view + OTel `patchy.stage.tokens` / `patchy.stage.cost`.

---

## One-liner

!!! warning ""

    **GitHub Advanced Security CodeQL is the path you’ll see in this demo. It is one of several sources Patchy ships.**

---

## Source material

Drawn from Patchy product docs: `docs/concepts/how-it-works.md`, `docs/operate/status-page.md`,
`docs/concepts/lifecycle.md`, `docs/getting-started/verify.md`, `docs/extending.md`, and the investigation prompt
contract (`internal/templates`).
