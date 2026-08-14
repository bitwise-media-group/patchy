---
title: Demo script
template: demo.html
hide:
  - navigation
---

# Patchy demo script

<div class="pt-demo-lede" markdown>

**10–15 minutes.** Findings arrive → Patchy triage → verdicts (false positive / remediate / manual) → human review.

Timing assumes findings are already in flight or can be **Expedite**d — call out waits explicitly.

</div>

!!! warning "Say early, say once more at the close"

    **What we show live today is GitHub Advanced Security / CodeQL.** That is the first production source — not the only
    source Patchy is designed to support. Ingestion is pluggable (`pkg/source`); the rest of the pipeline is
    source-agnostic.

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
    Kubernetes custom resources are the state machine; GitHub issues are a one-way human projection. Findings
    accumulate, get context, get investigated by a sandboxed agent, and high-confidence fixes become pull requests
    for humans to merge. **CodeQL via GitHub Advanced Security is the first integrated source and the path in this
    demo — not the product boundary.**

---

## Act 0 — Frame the problem {#act-0}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">0:00–1:30</span> <span class="pt-demo-dur">~1.5 min</span>

</div>

!!! example "On screen"

    Patchy status **Findings** board (or a slide with the phase rail).

!!! quote "Talk track"

    Static analysis is cheap to run and expensive to triage. Scanners produce real signal _and_ a lot of noise. Most
    teams either ignore the backlog or burn senior time on every alert.

    Patchy is an end-to-end pipeline for **triaging and remediating security findings from multiple sources**.
    Kubernetes custom resources are the state machine; GitHub issues are a one-way human projection. Findings
    accumulate, get context, get investigated by a sandboxed agent, and high-confidence fixes become pull requests
    for humans to merge.

    **What we show today is GitHub Advanced Security / CodeQL** — that is the first production source:
    `code_scanning_alert` webhooks into the integration-controller. **It is not the only source the system is built
    for.** Ingestion goes through a **plugin seam** (`pkg/source`): each source normalizes alerts into the same
    `Finding` model — advisories, severity, locations, repository identity. Accumulation, tracking-issue projection,
    context enhancement, investigation, verdict routing, and remediation are **source-agnostic**. Adding another
    scanner or platform means implementing a handler and wiring deliveries — not rewriting the pipeline.

    On every tracking issue you’ll see `security-source: …` (today often `ghas`). That label is how humans and
    searches see _which_ origin produced the finding; the pipeline treats them the same after ingest.

!!! info "Phase rail · point once"

    <code class="pt-demo-rail-phases">Opened → Enhanced → Investigating → Queued → Remediating → InReview → Remediated</code>

    Plus terminals / holds: `Dismissed` · `HandedOff` · `AwaitingApproval` · `Failed`

!!! tip "Transition"

    “For this demo we’ll drive CodeQL, but keep in mind: same lifecycle for any source that can speak the Finding
    contract.”

---

## Act 1 — Push → CodeQL → dual visibility {#act-1}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">1:30–4:00</span> <span class="pt-demo-dur">~2.5 min</span>

</div>

### 1a. Push · ≈30s

!!! example "On screen"

    Demo repo → commit/push (or open a PR that will run analysis).

!!! quote "Talk track"

    We start where developers already work: a normal push to GitHub. No special CLI, no agent on the laptop. For
    **this** source path, the GitHub App’s single webhook URL points at Patchy’s **integration-controller** — the
    internet-facing entry for GitHub deliveries. Other sources would enter through the same controller surface via
    their own validated webhook/integration config — still one architectural front door, multiple handlers behind it.

### 1b. CodeQL pipeline · ≈45s

!!! example "On screen"

    Actions tab → CodeQL / code-scanning workflow running or completed.

!!! quote "Talk track"

    CodeQL analyzes the tree and opens or updates **code scanning alerts**. GitHub Advanced Security remains the
    scanner of record **for this path**. Patchy does not replace CodeQL — or any other scanner. It **consumes**
    findings: here, `code_scanning_alert` deliveries, normalized by the GHAS source handler (rule, severity,
    locations, CWE/CVE/GHSA) and folded into Findings.

    A future source — another SAST vendor, container scanning, dependency advisories, an internal scanner — would
    produce different raw payloads, but the same `Finding` resource and the same phases you’re about to watch.

### 1c. GitHub Security → Code scanning · ≈30s

!!! example "On screen"

    **Security → Code scanning** on the repo; open one alert.

!!! quote "Talk track"

    Same alerts developers and AppSec already know for **GHAS**. Other sources may surface in their own product UIs;
    Patchy’s job is the **shared triage and remediation layer** on top — one board, one priority model, one
    investigation report contract — regardless of origin.

### 1d. Patchy status page · ≈45s

!!! example "On screen"

    Status page **Findings** board; filter to the demo repo if needed.

!!! quote "Talk track"

    Within moments of the webhook, the same work shows up here: severity, phase, confidence (once investigated),
    verdict, age. Live updates over SSE — no refresh theater.

    Click into a finding: Overview, Alerts, Timeline, Remediation. Sidebar links the tracking issue, owners,
    advisories, and **source**. The board is not “the CodeQL UI” — it’s the **multi-source pipeline view**. Today
    every row may say GHAS; tomorrow the same columns can mix origins without a different product flow.

??? note "Optional · kubectl visible"

    `kubectl get findings -w` is the same state machine — phase, severity, priority, verdict — without the UI.
    Source lives on the CR and on `security-source` labels.

!!! tip "Demo tip · accumulation"

    Multiple alerts of the same advisory family against the same repo **accumulate for ~1 hour** into one Finding
    (condition `AccumulationComplete`, not a phase). Say so if the board shows growing alert counts without a phase
    change. For live demos, **Expedite** skips accumulation window + min-age so the gate can admit immediately.

---

## Act 2 — Context enhancement {#act-2}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">4:00–5:30</span> <span class="pt-demo-dur">~1.5 min</span>

</div>

!!! example "On screen"

    Finding moving `Opened → Enhanced` (status page phase or tracking-issue comment).

!!! quote "Talk track"

    Before any agent reads code, the **context-controller** runs an enhancer chain. Ownership and infrastructure
    relationships are recorded as enrichments on the Finding and projected as sticky comments / labels on the
    tracking issue.

    Today the built-in enhancer is a deliberate CMDB stand-in (YAML map); the `pkg/enhance` seam is where a real
    CMDB plugs in. Important: enhancement holds **no GitHub credential** — it only writes Finding status. Projection
    stays with the integration-controller.

    This stage is **source-agnostic**: whatever origin created the Finding gets the same ownership context. When the
    investigation report lands, humans already know _who owns this_ and _where it lives_, not just _what the scanner
    said_.

!!! success "Beat"

    Phase pill flips to **Enhanced**; enrichment comment visible on the issue if you open it.

---

## Act 3 — Investigation & the three ratings {#act-3}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">5:30–8:30</span> <span class="pt-demo-dur">~3 min</span>

</div>

!!! example "On screen"

    Phase → **Investigating**; optional agent Job logs (`PATCHY-EVENT:` stream) or finding detail Timeline.

!!! quote "Talk track"

    After accumulation closes and the finding is old enough (default **one hour** — our patience knob), the
    **investigation-controller** admits it: pin the repository SHA once, create an immutable `Investigation`,
    schedule a sandboxed agent Job.

    Isolation in one sentence: the agent pod has **no credential of any kind**, no Kubernetes API access, and no
    arbitrary internet — only a digest-verified tarball of the repo, with model traffic authenticated at the egress
    broker. Investigation and remediation see the **same pinned tree**.

    Investigation does not care whether the finding arrived from CodeQL or another handler: the agent gets a
    templated finding handoff and a SHA-pinned tree. Ratings, recommendation, and confidence are the same contract
    for every source.

### The three ratings · must cover

!!! quote "Talk track"

    The agent does not only say “true / false.” It rates three dimensions, each
    `none | low | medium | high | critical`, with a short justification:

| Dimension          | Question the agent answers                                                                       |
| ------------------ | ------------------------------------------------------------------------------------------------ |
| **Exploitability** | Can this actually be exercised _in this codebase_? Reachable entry points — or why none exist.   |
| **Likelihood**     | How probable is exploitation _in this deployment_? Exposure, preconditions, known exploit paths. |
| **Impact**         | What’s the blast radius if it fires? Data, privilege, lateral movement, availability.            |

    These ratings feed a **0–100 scheduling priority** when work queues for remediation:

    **severity 30% · exploitability 30% · likelihood 20% · impact 20%** (tunable).

    Scanner severity alone is not enough — something “high” on the scanner but unreachable should not steal slots
    from something medium but live on an internet-facing path. Inflating ratings steals capacity from genuinely
    urgent work; the prompt is explicit about that.

### Confidence scoring · must cover

!!! quote "Talk track"

    Separately, the agent sets **`confidence` ∈ [0.0, 1.0]**:

    - Probability that the **recommendation is correct**.
    - For **`remediate`**, specifically: probability the finding can be **fully fixed without breaking functionality**.

    That number is the automated-remediation gate. Default threshold is **0.75**:

    | Confidence path | What happens |
    | --------------- | ------------ |
    | `remediate` **≥ 0.75** and no breaking-change hold | **Queued** automatically |
    | `remediate` **&lt; 0.75** _or_ better-but-breaking alternative | **AwaitingApproval** until `/approve` (issue or status page) |

    Backwards-compatible fixes are always preferred. If a cleaner fix would break callers, the report can still
    recommend the safe path, set `breaking_change_available: true`, and describe the better option for a human.

### The three recommendations

| Recommendation  | Meaning                                                                      | Terminal / next phase                                                  |
| --------------- | ---------------------------------------------------------------------------- | ---------------------------------------------------------------------- |
| **`ignore`**    | False positive (not exploitable, bad dataflow assumption, already mitigated) | **Dismissed** — source alerts dismissed where applicable, issue closed |
| **`remediate`** | Real issue; automated fix likely to succeed                                  | **Queued** / **AwaitingApproval** / then agent fix                     |
| **`manual`**    | Real issue; needs domain judgment or is too risky to automate                | **HandedOff** to owners (revivable via `/approve`)                     |

!!! tip "Transition"

    “We’ll walk each path. Start with the cheap win: false positives.”

---

## Act 4 — False positive: early exit & token cost {#act-4}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">8:30–10:30</span> <span class="pt-demo-dur">~2 min</span>

</div>

!!! example "On screen"

    Finding with recommendation **ignore** → phase **Dismissed**; GitHub code-scanning alert dismissed as false
    positive (for the GHAS path); rollups if useful.

!!! quote "Talk track"

    When the agent can _prove_ the finding is not real — dead path, sanitizer already in place, tool misunderstanding
    the dataflow — it recommends **`ignore`**. Controllers dismiss the accumulated alerts as false positive (for
    GHAS: Code scanning alerts) and close the tracking issue. Phase lands at **Dismissed**. No remediation Job. No PR
    noise. No human backlog entry that will never be fixed.

    **This is the economic early exit.** Investigation is deliberately cheaper than remediation:

| Stage       | Default ceilings (order of magnitude)                      |
| ----------- | ---------------------------------------------------------- |
| Investigate | ~**25** turns · **~150k** token budget · ~15m timeout      |
| Remediate   | up to **80** turns · **~400k** token budget · ~45m timeout |

    Remediation also _requests_ its own model / turns / budget in the investigation report; those are **clamped** to
    operator ceilings and a model allowlist. A false positive never opens that second stage.

    On the **Rollups** tab (public wallboard-friendly aggregates): per-stage **token / cost / duration**, verdict mix,
    terminal phases. Point at an `ignore` share and investigation-stage cost vs remediation-stage cost:

    > Every false positive we stop at investigation is a remediation run we never pay for — and a PR reviewers never
    > context-switch for.

    Machine accounting also lives on the CRs (`Investigations`, `FindingRollups`); status rollups are the friendly
    view of the same story.

??? note "Optional beat"

    Open the investigation report on the issue — exploitability often `none`/`low` with a crisp “why not reachable”
    summary. That justification is what AppSec would have written by hand.

---

## Act 5 — Remediation: agents open PRs {#act-5}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">10:30–13:00</span> <span class="pt-demo-dur">~2.5 min</span>

</div>

!!! example "On screen"

    Finding with **`remediate`**, confidence ≥ 0.75 → **Queued → Remediating → InReview**; open the PR.

!!! quote "Talk track"

    For a real, automatable finding above the confidence bar, the finding enters the remediation queue. The
    **remediation-controller** schedules in **priority order** under bounded concurrency (with aging so low-priority
    work doesn’t starve forever).

    A second sandboxed agent gets: finding markdown, the **same** pinned tree, and the investigation report — under
    the clamped budget. It must produce a summary report **and** a `commit.sh` that actually leaves commits. The
    controller **verifies the claim**; it doesn’t trust the model’s word.

    Critically: the agent still has **no write credential**. Only the remediation-controller holds a short-lived,
    scoped forge write token. It replays the changeset through the GitHub API onto a branch named
    **`patchy/<finding>`**, **opens the pull request**, and moves the finding to **InReview**.

    Humans stay in the merge path. Merging fires a webhook → **Remediated**. Closing the PR unmerged → **Failed**
    (retryable from the status page).

!!! example "On screen · PR body"

    Review this like any other PR — diff, CI, ownership. Patchy automated the _investigation_ and the _proposal_;
    merge remains a human judgment call. That separation is intentional: automated fix generation, human acceptance.

??? warning "If confidence is low or breaking-change hold"

    Show **AwaitingApproval** → click **Approve** on the status page (or `/approve` on the issue) → same machine edge
    into **Queued**. Status server never moves phases itself; it records approval; the controller owns the edge.

---

## Act 6 — Manual path + close {#act-6}

<div class="pt-demo-act-meta" markdown>

<span class="pt-demo-clock">13:00–14:30</span> <span class="pt-demo-dur">~1.5 min</span>

</div>

!!! example "On screen"

    Finding with recommendation **`manual`** → **HandedOff** (if available); otherwise narrate.

!!! quote "Talk track"

    Not everything should be auto-fixed: auth redesigns, ambiguous product risk, multi-service coordination.
    **`manual`** hands the finding to repository owners via the tracking issue — **HandedOff** — with the full
    investigation report as context. A later `/approve` can still revive it into the queue if someone decides
    automation should try.

    Humans can also close the tracking issue at any non-terminal phase → **HandedOff** — always able to pull work out
    of the machine.

!!! success "Wrap · 30–45s"

    End-to-end for this demo: **push → CodeQL → alerts in GitHub Security and on Patchy’s status page → context →
    investigation with three ratings and a confidence score → early-exit dismissals that save remediation tokens →
    high-confidence remediations that open real PRs → humans merge.**

    The **scanner stays the scanner** — today CodeQL via GitHub Advanced Security, tomorrow whatever you plug into
    the source seam. Patchy is the **extensible triage and safe auto-remediation layer** on top: multi-source by
    design, stateful, priority-aware, credential-isolated, and measurable in rollups.

---

## Timing card {#timing-card}

<div class="pt-demo-section-label" markdown>

Presenter cheat sheet · glance while talking

</div>

|       Clock | Act                                  | Key visual                  | Callout                    |
| ----------: | ------------------------------------ | --------------------------- | -------------------------- |
|   0:00–1:30 | Problem + **multi-source framing**   | Status board                | GHAS first; plugin seam    |
|   1:30–4:00 | Push → CodeQL → dual surface         | Actions + Security + status | “This path today”          |
|   4:00–5:30 | Context enhancement                  | `Opened → Enhanced`         | Source-agnostic stage      |
|   5:30–8:30 | Investigation, 3 ratings, confidence | Investigating               | Same report for any source |
|  8:30–10:30 | False positive early exit + cost     | Dismissed + Rollups         | Economic early exit        |
| 10:30–13:00 | Remediation opens PRs                | PR `patchy/<finding>`       | Verify-don’t-trust         |
| 13:00–14:30 | Manual + wrap                        | HandedOff / summary         | Restate extensibility      |

<div class="pt-demo-flex" markdown>

!!! tip "Running short"

    Cut Act 6 detail; keep multi-source framing, the three ratings, confidence threshold (0.75), FP cost contrast,
    and PR open.

!!! tip "Running long"

    Show Approve path, Expedite, or `kubectl get findings -w` briefly.

</div>

---

## Suggested live path

1. **False positive first** — proves early exit and cost story while remediation runs in background if needed.
2. **High-confidence remediate** — open the PR while talking isolation + verify-don’t-trust.
3. **Manual or AwaitingApproval** — shows humans stay in control.

---

## Audience Q&A {#qa}

<div class="pt-demo-section-label" markdown>

Optional · have ready, don’t force

</div>

??? question "Does Patchy replace CodeQL?"

    No. CodeQL/GHAS is the **first** integrated source, not the product boundary. Patchy ingests findings through a
    **source-agnostic** `Finding` model and a **plugin seam** (`pkg/source`); other scanners and platforms are meant
    to be added as handlers without reworking accumulation, investigation, or remediation.

??? question "What does multi-source look like on the board?"

    Same phases and verdicts; origin is explicit via `security-source` (and finding metadata). Humans filter by source
    when they care; the pipeline doesn’t fork by vendor after ingest.

??? question "What if a source has no GitHub alerts UI?"

    Projection is still the tracking issue + status page; forge write/read for remediation depends on a configured
    Forge for that repository — findings without a remediable forge can still be triaged (investigate / dismiss /
    hand off) even if they never reach the PR stage.

??? question "What if the agent is wrong?"

    Confidence gate, AwaitingApproval, human merge on PRs, dismissals are revivable (reopen issue → HandedOff).

??? question "Where’s the state?"

    Kubernetes CRs only; issues/labels never parsed back as truth.

??? question "How do we see spend?"

    Rollups + OTel `patchy.stage.tokens` / `patchy.stage.cost`.

---

## One-liner

!!! warning ""

    **GitHub Advanced Security CodeQL is the first source we ship and the path you’ll see in this demo — not the only
    source Patchy is designed to support.**

---

## Source material

Drawn from Patchy product docs: `docs/how-it-works.md`, `docs/status-ui.md`, `docs/labels.md`,
`docs/getting-started/verify.md`, `docs/extending.md`, and the investigation prompt contract (`internal/templates`).
