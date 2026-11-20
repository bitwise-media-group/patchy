// Monthly trend — single-series magnitude, so one hue and no legend; the
// toggle names the measure. The peak month is labelled directly, every bar
// carries its value on hover, and a visually hidden table restates the
// series for screen readers.

import { useState } from "preact/hooks";
import type { MonthlyBucket } from "../types";
import { formatCount, formatMicroUSD, formatMonth } from "../format";

type Measure = "findings" | "runs" | "cost";

const MEASURES: [Measure, string][] = [
  ["findings", "Findings"],
  ["runs", "Runs"],
  ["cost", "Spend"],
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
  const name = MEASURES.find(([m]) => m === measure)![1];

  return (
    <section class="rounded-[10px] border border-line bg-surface px-5 py-4.5" aria-labelledby="metrics-trend">
      <header class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="metrics-trend" class="ps-heading">
            Month by month
          </h2>
          <p class="mt-1 mb-0 text-[13px] text-muted">{name} per calendar month.</p>
        </div>
        <div class="ps-filter-group" role="group" aria-label="Measure">
          {MEASURES.map(([m, l]) => (
            <button
              key={m}
              type="button"
              class={measure === m ? "is-active" : ""}
              aria-pressed={measure === m}
              onClick={() => setMeasure(m)}
            >
              {l}
            </button>
          ))}
        </div>
      </header>
      <div class="mt-3 flex h-[170px] items-end gap-1.5 border-b border-line pt-6" aria-hidden="true">
        {months.map((m, i) => (
          <div class="group relative flex h-full flex-1 flex-col items-center justify-end" key={m} title={`${formatMonth(m)}: ${label(values[i], measure)}`}>
            <span
              class={`absolute font-mono text-[12px] whitespace-nowrap text-fg ${i === peak ? "" : "hidden group-hover:block"}`}
              style={{ bottom: `calc(${Math.max(2, (values[i] / max) * 100)}% + 4px)` }}
            >
              {label(values[i], measure)}
            </span>
            <span
              class={`w-[min(28px,72%)] rounded-t-[3px] transition-[height] duration-200 ${i === peak ? "bg-turf" : "bg-moss group-hover:bg-turf"}`}
              style={{ height: `${Math.max(2, (values[i] / max) * 100)}%` }}
            />
          </div>
        ))}
      </div>
      <div class="mt-1.5 flex gap-1.5" aria-hidden="true">
        {months.map((m, i) => (
          <span
            key={m}
            class={`flex-1 text-center font-mono text-[12.5px] whitespace-nowrap text-muted ${i % 2 === 1 && i !== months.length - 1 ? "max-xl:invisible" : ""}`}
          >
            {formatMonth(m)}
          </span>
        ))}
      </div>
      <table class="sr-only">
        <caption>{name} per month</caption>
        <thead>
          <tr>
            <th scope="col">Month</th>
            <th scope="col">{name}</th>
          </tr>
        </thead>
        <tbody>
          {months.map((m, i) => (
            <tr key={m}>
              <th scope="row">{formatMonth(m)}</th>
              <td>{label(values[i], measure)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
