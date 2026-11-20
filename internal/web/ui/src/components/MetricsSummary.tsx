// The metrics summary for whatever is in view: everything, one repository,
// a set of matching repositories, one model, or one harness. One layout
// for every slice: a sentence that says what happened, the outcome strip,
// the key figures (with the slice's share of the whole when sliced), then
// verdicts, the monthly trend and the per-stage detail.
//
// Repository-shaped buckets own findings; model and harness shards carry
// only stage aggregates, so their headline is about runs instead.

import type { ComponentChildren } from "preact";
import type { Rollup, StageAggregate } from "../types";
import {
  DASH,
  formatCount,
  formatDay,
  formatMicroUSD,
  formatMs,
  formatPercent,
  formatSkew,
  formatTokens,
  skew,
  skewTone,
} from "../format";
import {
  attemptsPerFinding,
  carriesFindings,
  costPerRemediation,
  remediatedShare,
  runOutcomes,
  shortName,
  stageTotals,
  type RollupSlice,
} from "../rollups";
import { OutcomeStrip, phaseOutcomes, runOutcomeList } from "./OutcomeStrip";
import { TrendBars } from "./TrendBars";

const CARD = "rounded-[10px] border border-line bg-surface px-5 py-4.5";

const VERDICT_TONES: Record<string, string> = { remediate: "bg-turf", ignore: "bg-soil", manual: "bg-amber" };
const VERDICT_LABELS: Record<string, string> = {
  remediate: "Remediate",
  ignore: "Ignore",
  manual: "Needs a person",
};

function Verdicts({ data }: { data?: Record<string, number> }) {
  const entries = Object.entries(data ?? {}).sort((a, b) => b[1] - a[1]);
  const total = entries.reduce((a, [, v]) => a + v, 0);
  return (
    <section class={CARD} aria-labelledby="metrics-verdicts">
      <h2 id="metrics-verdicts" class="ps-heading">
        Investigation verdicts
      </h2>
      <p class="mt-1 mb-4 text-[13px] text-muted">What the agent recommended for each finding.</p>
      {total === 0 ? (
        <p class="m-0 text-faint">No verdicts recorded.</p>
      ) : (
        <dl class="m-0 flex flex-col gap-3">
          {entries.map(([k, v]) => (
            <div key={k}>
              <div class="flex items-baseline justify-between gap-3 text-[13.5px]">
                <dt>{VERDICT_LABELS[k] ?? k}</dt>
                <dd class="m-0 font-mono text-muted">
                  {formatCount(v)} <span class="text-faint">({formatPercent(v / total)})</span>
                </dd>
              </div>
              <span class="mt-1.5 block h-1.5 overflow-hidden rounded-full bg-code" aria-hidden="true">
                <span class={`block h-full rounded-full ${VERDICT_TONES[k] ?? "bg-faint"}`} style={{ width: `${(v / total) * 100}%` }} />
              </span>
            </div>
          ))}
        </dl>
      )}
    </section>
  );
}

// EstimateAccuracy reports how well the investigation predicted this stage's
// cost. Predicted and actual sums cover the same runs, so the skew is
// like-for-like; a stage with no estimate history renders nothing.
function EstimateAccuracy({ stage }: { stage: StageAggregate }) {
  const e = stage.estimate;
  if (!e?.runs) return null;
  const rows = [
    {
      label: "Turns, estimated against actual",
      predicted: (e.predictedTurns ?? 0) / e.runs,
      actual: (e.actualTurns ?? 0) / e.runs,
      format: (n: number) => formatCount(Math.round(n)),
    },
    {
      label: "Output tokens, estimated against actual",
      predicted: (e.predictedOutputTokens ?? 0) / e.runs,
      actual: (e.actualOutputTokens ?? 0) / e.runs,
      format: (n: number) => formatTokens(Math.round(n)),
    },
  ];
  return (
    <>
      {rows.map((r) => {
        const s = skew(r.predicted, r.actual);
        const tone = skewTone(s);
        return (
          <div key={r.label}>
            <dt>{r.label}</dt>
            <dd>
              <span class="font-mono">
                {r.format(r.predicted)} / {r.format(r.actual)}
              </span>{" "}
              <span
                class={`font-mono ${tone === "red" ? "text-red" : tone === "amber" ? "text-amber" : "text-muted"}`}
                title={`Averaged over the ${e.runs} run(s) that carried an estimate`}
              >
                {formatSkew(s)}
              </span>
            </dd>
          </div>
        );
      })}
    </>
  );
}

export function StageCard({ name, blurb, stage }: { name: string; blurb: string; stage?: StageAggregate }) {
  const id = `stage-${name.toLowerCase()}`;
  if (!stage?.runs) {
    return (
      <section class={CARD} aria-labelledby={id}>
        <h2 id={id} class="ps-heading">
          {name}
        </h2>
        <p class="mt-1 mb-0 text-[13px] text-muted">No runs recorded.</p>
      </section>
    );
  }
  const avg = (n?: number) => (n ?? 0) / stage.runs!;
  return (
    <section class={CARD} aria-labelledby={id}>
      <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h2 id={id} class="ps-heading">
          {name}
        </h2>
        <span class="font-mono text-[13px] text-muted">{formatCount(stage.runs)} runs</span>
      </div>
      <p class="mt-1 mb-4 text-[13px] text-muted">{blurb}</p>
      <OutcomeStrip items={runOutcomeList(runOutcomes({ stages: { s: stage } }))} label={`${name} runs by outcome`} thin />
      <dl class="ps-kv mt-4.5">
        <div>
          <dt>Succeeded</dt>
          <dd class="font-mono">{formatPercent((stage.succeeded ?? 0) / stage.runs)}</dd>
        </div>
        <div>
          <dt>Spend</dt>
          <dd class="font-mono">{formatMicroUSD(stage.costMicroUSD)}</dd>
        </div>
        <div>
          <dt>Per run</dt>
          <dd class="font-mono">{stage.costMicroUSD !== undefined ? formatMicroUSD(avg(stage.costMicroUSD)) : DASH}</dd>
        </div>
        <div>
          <dt>Average run time</dt>
          <dd class="font-mono">{formatMs(stage.elapsedMilliseconds !== undefined ? avg(stage.elapsedMilliseconds) : undefined)}</dd>
        </div>
        <div>
          <dt>Average turns</dt>
          <dd class="font-mono">{stage.turns !== undefined ? formatCount(Math.round(avg(stage.turns))) : DASH}</dd>
        </div>
        <div>
          <dt>Tokens in / out</dt>
          <dd class="font-mono">
            {formatTokens(stage.inputTokens)} / {formatTokens(stage.outputTokens)}
          </dd>
        </div>
        <div>
          <dt>Cache read</dt>
          <dd class="font-mono">{formatTokens(stage.cacheReadTokens)}</dd>
        </div>
        <EstimateAccuracy stage={stage} />
      </dl>
    </section>
  );
}

function Figures({ items }: { items: [string, string, string?][] }) {
  return (
    <dl
      class="m-0 grid grid-cols-5 overflow-hidden rounded-[10px] border border-line bg-surface max-lg:grid-cols-2 max-lg:[&>.ps-stat]:odd:border-l-0 max-lg:[&>.ps-stat:nth-child(n+3)]:border-t max-lg:[&>.ps-stat:nth-child(n+3)]:border-t-line"
    >
      {items.map(([label, value, note]) => (
        <div class="ps-stat flex flex-col-reverse justify-end" key={label}>
          <dt class="mt-1 text-[13px] text-muted">
            {label}
            {note ? <span class="block text-[12.5px] text-faint">{note}</span> : null}
          </dt>
          <dd class="m-0 font-mono text-[24px] leading-tight font-bold text-fg">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

// share is a slice's part of the whole. One repository among thousands is
// often under half a percent, which would round to a misleading "0%".
const share = (part: number | undefined, whole: number | undefined) => {
  if (part === undefined || !whole) return DASH;
  const r = part / whole;
  return r > 0 && r < 0.005 ? "<1%" : formatPercent(r);
};

export function MetricsSummary({
  total,
  repositoryCount,
  rank,
  slice,
  rollup,
  members,
}: {
  // The total shard, for each slice's share of the whole.
  total?: Rollup;
  repositoryCount: number;
  // A repository's position by findings closed.
  rank?: number;
  slice: RollupSlice | null;
  rollup: Rollup;
  // Repositories a "match" slice summed.
  members?: number;
}) {
  const b = rollup.bucket;
  const totals = stageTotals(b);
  const whole = total?.bucket;
  const wholeTotals = whole ? stageTotals(whole) : undefined;
  const since = rollup.firstProcessed ? formatDay(rollup.firstProcessed) : undefined;

  let headline: ComponentChildren;
  let strip: ComponentChildren;
  let figures: [string, string, string?][];

  if (carriesFindings(slice)) {
    const remediated = b.phases?.remediated ?? 0;
    const where =
      slice === null
        ? null
        : slice.kind === "match"
          ? `across ${formatCount(members)} repositories matching “${slice.key}”`
          : `in ${shortName(slice.key)}`;
    headline = (
      <>
        {formatCount(b.findings ?? 0)} findings closed{where ? ` ${where}` : ""}
        {since && slice === null ? ` since ${since}` : ""}.{" "}
        <span class="text-muted">
          {formatCount(remediated)} of them were remediated with a pull request.
        </span>
      </>
    );
    strip = <OutcomeStrip items={phaseOutcomes(b.phases ?? {})} label="Findings by outcome" />;
    const cpr = costPerRemediation(b);
    const apf = attemptsPerFinding(b);
    figures = [
      ["Remediated", formatPercent(remediatedShare(b))],
      ["Agent runs", formatCount(totals.runs)],
      ["Spend", totals.costMicroUSD ? formatMicroUSD(totals.costMicroUSD) : DASH],
      ["Cost per remediation", cpr !== undefined ? formatMicroUSD(cpr) : DASH],
      ["Attempts per finding", apf !== undefined ? apf.toFixed(2) : DASH],
    ];
    if (slice !== null) {
      figures = [
        figures[0],
        [
          "Share of all findings",
          share(b.findings, whole?.findings),
          slice.kind === "repository" && rank ? `Ranked ${formatCount(rank)} of ${formatCount(repositoryCount)}` : undefined,
        ],
        figures[2],
        ["Share of spend", share(totals.costMicroUSD, wholeTotals?.costMicroUSD)],
        figures[3],
      ];
    }
  } else {
    const ok = totals.runs ? totals.succeeded / totals.runs : undefined;
    headline = (
      <>
        {formatCount(totals.runs)} agent runs on <span class="font-mono">{slice!.key}</span>.{" "}
        <span class="text-muted">{formatPercent(ok)} of them succeeded.</span>
      </>
    );
    strip = <OutcomeStrip items={runOutcomeList(runOutcomes(b))} label="Runs by outcome" />;
    figures = [
      ["Share of all runs", share(totals.runs, wholeTotals?.runs)],
      ["Spend", totals.costMicroUSD ? formatMicroUSD(totals.costMicroUSD) : DASH],
      ["Share of spend", share(totals.costMicroUSD, wholeTotals?.costMicroUSD)],
      ["Cost per run", totals.runs ? formatMicroUSD(totals.costMicroUSD / totals.runs) : DASH],
      ["Average run time", formatMs(totals.runs ? totals.elapsedMilliseconds / totals.runs : undefined)],
    ];
  }

  const stages = (
    <>
      <StageCard
        name="Investigation"
        blurb="Agent runs that read the code and decide what a finding needs."
        stage={b.stages?.investigation}
      />
      <StageCard
        name="Remediation"
        blurb="Agent runs that write the change patchy proposes as a pull request."
        stage={b.stages?.remediation}
      />
    </>
  );

  return (
    <div class="flex flex-col gap-4">
      <section class={`${CARD} px-6 pt-6 pb-5`} aria-labelledby="metrics-headline">
        <p
          id="metrics-headline"
          class="m-0 mb-5 max-w-[34ch] text-[26px] leading-[1.25] font-bold tracking-[-0.01em] text-balance text-fg max-sm:text-[21px]"
        >
          {headline}
        </p>
        {strip}
      </section>

      <Figures items={figures} />

      {carriesFindings(slice) ? (
        rollup.monthly ? (
          <>
            <div class="grid grid-cols-[1fr_2fr] gap-4 max-lg:grid-cols-1">
              <Verdicts data={b.recommendations} />
              <TrendBars monthly={rollup.monthly} />
            </div>
            <div class="grid grid-cols-2 gap-4 max-lg:grid-cols-1">{stages}</div>
          </>
        ) : (
          <div class="grid grid-cols-3 gap-4 max-xl:grid-cols-2 max-lg:grid-cols-1">
            <Verdicts data={b.recommendations} />
            {stages}
          </div>
        )
      ) : (
        <>
          <div class="grid grid-cols-2 gap-4 max-lg:grid-cols-1">{stages}</div>
          <p class="m-0 max-w-[72ch] text-[13.5px] text-muted">
            A finding can pass through several models and harnesses, so outcomes and verdicts are counted per
            repository. Filter by a repository to see them.
          </p>
        </>
      )}
    </div>
  );
}
