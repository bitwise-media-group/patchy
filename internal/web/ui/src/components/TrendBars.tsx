// Monthly trend — single-series magnitude, so one hue (turf green) and no
// legend; the toggle names the measure. Values surface on hover and the
// peak month is direct-labeled.

import { useState } from "preact/hooks";
import type { MonthlyBucket } from "../types";
import { formatCount, formatMicroUSD, formatMonth } from "../format";

type Measure = "findings" | "runs" | "cost";

const MEASURES: [Measure, string][] = [
  ["findings", "findings"],
  ["runs", "runs"],
  ["cost", "cost"],
];

function value(bucket: MonthlyBucket, measure: Measure): number {
  if (measure === "findings") return bucket.findings ?? 0;
  if (measure === "runs") return bucket.runs ?? 0;
  return bucket.costMicroUSD ?? 0;
}

function label(v: number, measure: Measure): string {
  return measure === "cost" ? formatMicroUSD(v) : formatCount(v);
}

export function TrendBars({ monthly }: { monthly: Record<string, MonthlyBucket> }) {
  const [measure, setMeasure] = useState<Measure>("findings");
  const months = Object.keys(monthly).sort();
  if (months.length === 0) return null;
  const values = months.map((m) => value(monthly[m], measure));
  const max = Math.max(...values, 1);
  const peak = values.indexOf(Math.max(...values));

  return (
    <section class="mt-3.5 rounded-xl border border-line bg-surface px-4.5 py-4" aria-label="Monthly trend">
      <header class="flex flex-wrap items-center justify-between gap-3.5">
        <h2 class="ps-heading">Monthly trend</h2>
        <div class="ps-filter-group" role="group" aria-label="Measure">
          {MEASURES.map(([m, l]) => (
            <button key={m} type="button" class={measure === m ? "is-active" : ""} aria-pressed={measure === m} onClick={() => setMeasure(m)}>
              {l}
            </button>
          ))}
        </div>
      </header>
      <div class="mt-4 flex h-[150px] items-end gap-1.5 pt-4" role="img" aria-label={`${measure} per month`}>
        {months.map((m, i) => (
          <div
            class="group relative flex h-full flex-1 flex-col items-center justify-end gap-1.5"
            key={m}
            title={`${formatMonth(m)}: ${label(values[i], measure)}`}
          >
            {i === peak ? (
              <span class="absolute -top-4 font-mono text-[10px] whitespace-nowrap text-muted">{label(values[i], measure)}</span>
            ) : null}
            <span
              class="w-[min(26px,70%)] rounded-t bg-turf transition-[height] group-hover:bg-turf-2"
              style={{ height: `${Math.max(3, (values[i] / max) * 100)}%` }}
            />
            <span class="font-mono text-[9px] whitespace-nowrap text-faint max-lg:hidden">{formatMonth(m)}</span>
          </div>
        ))}
      </div>
    </section>
  );
}
