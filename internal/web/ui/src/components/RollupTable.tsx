// Per-scope rollup tables. Repository rows carry finding-level counters;
// harness/model rows carry only stage aggregates (a finding has no single
// harness/model owner) — absent figures render as dashes, not zeros.
// The phase-mix bar never carries identity alone: the share column and the
// tooltip restate the mix in text.

import type { Rollup, ScopeType, StageAggregate } from "../types";
import { DASH, formatCount, formatMicroUSD, formatMs, formatPercent, formatTokens } from "../format";

// Keys are the server's rollup histogram vocabulary (all lowercase —
// internal/stats.PhaseKey lower-cases the phase).
const MIX_ORDER: [string, string][] = [
  ["remediated", "green"],
  ["failed", "red"],
  ["dismissed", "soil"],
  ["handedoff", "amber"],
  ["deleted", "faint"],
];

function PhaseMixBar({ phases }: { phases?: Record<string, number> }) {
  if (!phases) return <span class="text-faint">{DASH}</span>;
  const total = Object.values(phases).reduce((a, b) => a + b, 0);
  if (total === 0) return <span class="text-faint">{DASH}</span>;
  const title = MIX_ORDER.filter(([k]) => phases[k])
    .map(([k]) => `${k} ${phases[k]}`)
    .join(" · ");
  return (
    <span class="ps-mixbar" title={title}>
      {MIX_ORDER.filter(([k]) => phases[k]).map(([k, tone]) => (
        <span key={k} class={`ps-fill--${tone}`} style={{ width: `${(phases[k] / total) * 100}%` }} />
      ))}
    </span>
  );
}

type StageTotals = Required<
  Pick<StageAggregate, "runs" | "succeeded" | "inputTokens" | "outputTokens" | "costMicroUSD" | "elapsedMilliseconds">
>;

function sumStages(rollup: Rollup): StageTotals {
  const stages = Object.values(rollup.bucket.stages ?? {});
  const out: StageTotals =
    { runs: 0, succeeded: 0, inputTokens: 0, outputTokens: 0, costMicroUSD: 0, elapsedMilliseconds: 0 };
  for (const s of stages) {
    out.runs += s.runs ?? 0;
    out.succeeded += s.succeeded ?? 0;
    out.inputTokens += s.inputTokens ?? 0;
    out.outputTokens += s.outputTokens ?? 0;
    out.costMicroUSD += s.costMicroUSD ?? 0;
    out.elapsedMilliseconds += s.elapsedMilliseconds ?? 0;
  }
  return out;
}

export function RollupTable({ scope, rollups }: { scope: ScopeType; rollups: Rollup[] }) {
  if (rollups.length === 0) {
    return <div class="px-5 py-11 text-center text-muted">No {scope} rollups yet — stats appear once findings complete.</div>;
  }
  const isRepo = scope === "repository";
  const grid = isRepo ? "ps-grid-rollup-repo" : "ps-grid-rollup";
  const sorted = [...rollups].sort((a, b) => {
    const av = isRepo ? (a.bucket.findings ?? 0) : sumStages(a).runs;
    const bv = isRepo ? (b.bucket.findings ?? 0) : sumStages(b).runs;
    return bv - av;
  });

  return (
    <section class="overflow-hidden rounded-xl border border-line-2 bg-surface shadow-card max-lg:overflow-x-auto" aria-label={`Rollups by ${scope}`}>
      <div class={`${grid} ps-table-header`} aria-hidden="true">
        <span>{scope}</span>
        {isRepo ? (
          <>
            <span>Findings</span>
            <span>Phase mix</span>
            <span>Remediated</span>
            <span>Attempts</span>
          </>
        ) : (
          <>
            <span>Runs</span>
            <span>Success</span>
            <span>Tokens in / out</span>
            <span>Avg run time</span>
          </>
        )}
        <span>Cost</span>
      </div>
      {sorted.map((r) => {
        const key = r.scope.key ?? "";
        const stages = sumStages(r);
        const success = stages.runs ? stages.succeeded / stages.runs : undefined;
        const remediatedShare =
          r.bucket.findings && r.bucket.phases?.remediated !== undefined
            ? r.bucket.phases.remediated / r.bucket.findings
            : undefined;
        return (
          <div class={`${grid} ps-hover-row min-h-14 px-4.5 font-mono text-[11.5px] text-fg`} key={key}>
            <span class="font-semibold [overflow-wrap:anywhere]">{key || "total"}</span>
            {isRepo ? (
              <>
                <span>{formatCount(r.bucket.findings)}</span>
                <PhaseMixBar phases={r.bucket.phases} />
                <span>{formatPercent(remediatedShare)}</span>
                <span>{formatCount(r.bucket.attempts)}</span>
              </>
            ) : (
              <>
                <span>{formatCount(stages.runs || undefined)}</span>
                <span>{formatPercent(success)}</span>
                <span>
                  {formatTokens(stages.inputTokens || undefined)} / {formatTokens(stages.outputTokens || undefined)}
                </span>
                <span>{formatMs(stages.runs ? stages.elapsedMilliseconds / stages.runs : undefined)}</span>
              </>
            )}
            <span>{formatMicroUSD(stages.costMicroUSD || undefined)}</span>
          </div>
        );
      })}
    </section>
  );
}
