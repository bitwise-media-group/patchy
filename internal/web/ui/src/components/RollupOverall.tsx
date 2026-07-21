// The total-scope rollup: all-time headline tiles, terminal-phase and
// verdict mixes as labeled bar rows (identity lives in the row label and
// count — the status hue only reinforces), per-stage aggregates, and the
// monthly trend.

import type { Rollup, StageAggregate } from "../types";
import {
  DASH,
  formatCount,
  formatMicroUSD,
  formatMs,
  formatPercent,
  formatTokens,
} from "../format";
import { TrendBars } from "./TrendBars";

// Keys are the server's rollup histogram vocabulary (all lowercase —
// internal/stats.PhaseKey lower-cases the phase).
const PHASE_MIX_TONES: Record<string, string> = {
  remediated: "green",
  failed: "red",
  dismissed: "soil",
  handedoff: "amber",
  deleted: "faint",
};

const VERDICT_MIX_TONES: Record<string, string> = {
  remediate: "green",
  ignore: "soil",
  manual: "amber",
};

const CARD = "rounded-xl border border-line bg-surface px-4.5 py-4";

export function BarRows({
  data,
  tones,
  ariaLabel,
}: {
  data: Record<string, number>;
  tones: Record<string, string>;
  ariaLabel: string;
}) {
  const entries = Object.entries(data).sort((a, b) => b[1] - a[1]);
  const max = Math.max(...entries.map(([, v]) => v), 1);
  return (
    <div class="flex flex-col gap-2" role="img" aria-label={ariaLabel}>
      {entries.map(([key, v]) => (
        <div class="grid grid-cols-[92px_1fr_46px] items-center gap-2.5" key={key}>
          <span class="font-mono text-[10.5px] text-muted [overflow-wrap:anywhere]">{key}</span>
          <span class="block h-2 overflow-hidden rounded-full bg-code-2">
            <span
              class={`block h-full rounded-full ps-fill--${tones[key] ?? "green"}`}
              style={{ width: `${(v / max) * 100}%` }}
            />
          </span>
          <span class="text-right font-mono text-[11px] text-fg">{formatCount(v)}</span>
        </div>
      ))}
    </div>
  );
}

function successRate(stage?: StageAggregate): number | undefined {
  if (!stage?.runs) return undefined;
  return (stage.succeeded ?? 0) / stage.runs;
}

export function StageCard({ name, stage }: { name: string; stage?: StageAggregate }) {
  if (!stage) {
    return (
      <section class={CARD}>
        <h3 class="ps-heading mb-3">{name}</h3>
        <p class="text-faint">No runs recorded.</p>
      </section>
    );
  }
  const avgElapsed = stage.runs ? (stage.elapsedMilliseconds ?? 0) / stage.runs : undefined;
  const outcomes = Object.entries(stage.outcomes ?? {}).sort((a, b) => b[1] - a[1]);
  return (
    <section class={CARD}>
      <h3 class="ps-heading mb-3">{name}</h3>
      <dl class="ps-kv">
        <div>
          <dt>Runs</dt>
          <dd>{formatCount(stage.runs)}</dd>
        </div>
        <div>
          <dt>Success</dt>
          <dd>{formatPercent(successRate(stage))}</dd>
        </div>
        <div>
          <dt>Tokens in / out</dt>
          <dd>
            {formatTokens(stage.inputTokens)} / {formatTokens(stage.outputTokens)}
          </dd>
        </div>
        <div>
          <dt>Cache read</dt>
          <dd>{formatTokens(stage.cacheReadTokens)}</dd>
        </div>
        <div>
          <dt>Cost</dt>
          <dd>{formatMicroUSD(stage.costMicroUSD)}</dd>
        </div>
        <div>
          <dt>Avg run time</dt>
          <dd>{formatMs(avgElapsed)}</dd>
        </div>
      </dl>
      {outcomes.length > 0 ? (
        <p class="mx-0 mt-3 mb-0 font-mono text-[10.5px] text-faint">
          {outcomes.map(([k, v]) => `${k} ${v}`).join(" · ")}
        </p>
      ) : null}
    </section>
  );
}

export function RollupOverall({ rollup }: { rollup: Rollup }) {
  const b = rollup.bucket;
  const remediatedShare =
    b.findings && b.phases?.remediated !== undefined ? b.phases.remediated / b.findings : undefined;
  const totalCost =
    (b.stages?.investigation?.costMicroUSD ?? 0) + (b.stages?.remediation?.costMicroUSD ?? 0);
  const totalRuns = (b.stages?.investigation?.runs ?? 0) + (b.stages?.remediation?.runs ?? 0);

  const tiles: [string, string][] = [
    ["findings completed", formatCount(b.findings)],
    ["remediated share", formatPercent(remediatedShare)],
    ["agent runs", totalRuns ? formatCount(totalRuns) : DASH],
    ["total cost", totalCost ? formatMicroUSD(totalCost) : DASH],
    ["attempts", formatCount(b.attempts)],
  ];

  return (
    <div>
      <section
        class="grid grid-cols-5 overflow-hidden rounded-xl border border-line-2 bg-surface max-lg:grid-cols-2 max-lg:[&>.ps-stat]:odd:border-l-0 max-lg:[&>.ps-stat:nth-child(n+3)]:border-t max-lg:[&>.ps-stat:nth-child(n+3)]:border-t-line"
        aria-label="All-time totals"
      >
        {tiles.map(([label, v]) => (
          <div class="ps-stat" key={label}>
            <strong>{v}</strong>
            <span>{label}</span>
          </div>
        ))}
      </section>

      <div class="mt-3.5 grid grid-cols-2 gap-3.5 max-lg:grid-cols-1">
        <section class={CARD}>
          <h2 class="ps-heading mb-3">Terminal phases</h2>
          {b.phases ? <BarRows data={b.phases} tones={PHASE_MIX_TONES} ariaLabel="Findings by terminal phase" /> : <p class="text-faint">{DASH}</p>}
        </section>
        <section class={CARD}>
          <h2 class="ps-heading mb-3">Verdicts</h2>
          {b.recommendations ? (
            <BarRows data={b.recommendations} tones={VERDICT_MIX_TONES} ariaLabel="Findings by verdict" />
          ) : (
            <p class="text-faint">{DASH}</p>
          )}
        </section>
      </div>

      <div class="mt-3.5 grid grid-cols-2 gap-3.5 max-lg:grid-cols-1">
        <StageCard name="Investigation" stage={b.stages?.investigation} />
        <StageCard name="Remediation" stage={b.stages?.remediation} />
      </div>

      {rollup.monthly ? <TrendBars monthly={rollup.monthly} /> : null}
    </div>
  );
}
