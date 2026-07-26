// Agent-run accounting: the harness/model/tokens/cost rows the stage tabs
// embed in their run <dl>, and the always-present header badges that show
// the finding's cross-attempt totals without reflowing when data arrives.

import type { Budget, Finding, Usage } from "../types";
import {
  DASH,
  formatCount,
  formatMicroUSD,
  formatSkew,
  formatTokens,
  skew,
  skewTone,
  totalTokens,
  usageBreakdown,
} from "../format";
import { Pill } from "./Pills";

export function RunAccountingRows({ harness, model, usage }: { harness?: string; model?: string; usage?: Usage }) {
  return (
    <>
      <div>
        <dt>Harness</dt>
        <dd>{harness ? <span class="ps-mono-tag">{harness}</span> : DASH}</dd>
      </div>
      <div>
        <dt>Model</dt>
        <dd>{model ? <span class="ps-mono-tag">{model}</span> : DASH}</dd>
      </div>
      <div>
        <dt>Tokens</dt>
        <dd>
          <span class="font-mono">{formatTokens(totalTokens(usage))}</span>
          {usageBreakdown(usage) ? (
            <small class="mt-0.5 block font-mono text-[10px] text-muted">{usageBreakdown(usage)}</small>
          ) : null}
        </dd>
      </div>
      <div>
        <dt>Cost</dt>
        <dd>
          <span class="font-mono">{formatMicroUSD(usage?.costMicroUSD || undefined)}</span>
        </dd>
      </div>
    </>
  );
}

// BudgetRows shows the three-way budget picture for a remediation run:
// what the investigation predicted, what the run was granted, and what it
// actually spent. The estimate never limits the run — it only decides whether
// a human had to approve it — so showing it beside the grant is the only way
// to tell a good estimate from a lucky one.
export function BudgetRows({
  budget,
  numTurns,
  usage,
}: {
  budget?: Budget;
  numTurns?: number;
  usage?: Usage;
}) {
  if (!budget) return null;
  const rows = [
    {
      label: "Turns",
      predicted: budget.estimated?.maxTurns,
      granted: budget.grantedMaxTurns,
      actual: numTurns,
      format: formatCount,
    },
    {
      label: "Output tokens",
      predicted: budget.estimated?.tokenBudget,
      granted: budget.grantedTokenBudget,
      actual: usage?.outputTokens,
      format: formatTokens,
    },
  ];
  return (
    <>
      {rows.map((r) => {
        const s = skew(r.predicted, r.actual);
        return (
          <div key={r.label}>
            <dt>{r.label}</dt>
            <dd>
              <span class="font-mono">{r.format(r.actual)}</span>
              <span class="text-muted"> of {r.format(r.granted)} granted</span>
              {r.predicted ? (
                <small class="mt-0.5 block font-mono text-[10px] text-muted">
                  est. {r.format(r.predicted)}
                  {s !== undefined ? (
                    <span
                      class={
                        skewTone(s) === "red"
                          ? "text-red"
                          : skewTone(s) === "amber"
                            ? "text-amber"
                            : "text-muted"
                      }
                      title={`Actual run against the investigation's estimate of ${r.format(r.predicted)}`}
                    >
                      {" "}
                      ({formatSkew(s)})
                    </span>
                  ) : null}
                </small>
              ) : null}
            </dd>
          </div>
        );
      })}
    </>
  );
}

// UsageBadges always renders both pills — a finding with no runs yet shows
// dashes, so the header keeps its shape when accounting lands later.
export function UsageBadges({ finding }: { finding: Finding }) {
  const u = finding.totalUsage;
  return (
    <>
      <Pill tone="neutral" label="Tokens" title={usageBreakdown(u) ?? "No agent runs accounted yet"}>
        {formatTokens(totalTokens(u))}
      </Pill>
      <Pill tone="neutral" label="Cost" title="Total across every attempt of both stages">
        {formatMicroUSD(u?.costMicroUSD || undefined)}
      </Pill>
    </>
  );
}
