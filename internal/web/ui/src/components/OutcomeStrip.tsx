// How things ended, as one proportional strip with a legend beneath it.
// The strip is the at-a-glance read; the legend carries identity (label,
// count, share) in text, so nothing depends on telling the hues apart.

import { formatCount, formatPercent } from "../format";

export type Tone = "green" | "seedling" | "soil" | "amber" | "red" | "faint";

export interface Outcome {
  key: string;
  label: string;
  value: number;
  tone: Tone;
}

// Keys are the server's rollup histogram vocabulary, all lowercase
// (internal/stats.PhaseKey lower-cases the phase). Order is the reading
// order: the good outcome first, then closed-without-change, then the ones
// that needed people, then failure.
const PHASES: [string, string, Tone][] = [
  ["remediated", "Remediated", "green"],
  ["dismissed", "Dismissed", "soil"],
  ["handedoff", "Handed off", "amber"],
  ["failed", "Failed", "red"],
  ["deleted", "Deleted", "faint"],
];

const RUNS: [string, string, Tone][] = [
  ["ok", "Succeeded", "green"],
  ["changeset_too_large", "Change too large", "soil"],
  ["timeout", "Timed out", "amber"],
  ["error", "Errored", "red"],
  ["aborted", "Aborted", "faint"],
];

function humanize(key: string): string {
  const s = key.replace(/[_-]+/g, " ");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

function outcomes(data: Record<string, number>, known: [string, string, Tone][]): Outcome[] {
  const out: Outcome[] = known
    .filter(([k]) => (data[k] ?? 0) > 0)
    .map(([key, label, tone]) => ({ key, label, value: data[key], tone }));
  // Anything the vocabulary does not know yet still renders, neutrally.
  for (const [k, v] of Object.entries(data)) {
    if (v > 0 && !known.some(([key]) => key === k)) out.push({ key: k, label: humanize(k), value: v, tone: "faint" });
  }
  return out;
}

export const phaseOutcomes = (phases: Record<string, number>) => outcomes(phases, PHASES);
export const runOutcomeList = (runs: Record<string, number>) => outcomes(runs, RUNS);

const FILL: Record<Tone, string> = {
  green: "bg-turf",
  seedling: "bg-seedling",
  soil: "bg-soil",
  amber: "bg-amber",
  red: "bg-red",
  faint: "bg-faint",
};

export function describe(items: Outcome[], total: number): string {
  return items.map((o) => `${o.label} ${formatCount(o.value)} (${formatPercent(o.value / total)})`).join(", ");
}

export function OutcomeStrip({
  items,
  label,
  thin = false,
  legend = true,
}: {
  items: Outcome[];
  // What the strip measures, for assistive technology ("Findings by outcome").
  label: string;
  thin?: boolean;
  legend?: boolean;
}) {
  const total = items.reduce((a, o) => a + o.value, 0);
  if (total === 0) return null;
  return (
    <div>
      <div
        class={`ps-strip ${thin ? "ps-strip--thin" : ""}`}
        role="img"
        aria-label={`${label}: ${describe(items, total)}`}
        title={thin ? describe(items, total) : undefined}
      >
        {items.map((o) => (
          <span key={o.key} class={FILL[o.tone]} style={{ width: `${(o.value / total) * 100}%` }} />
        ))}
      </div>
      {legend ? (
        <ul class="m-0 mt-3 flex list-none flex-wrap gap-x-6 gap-y-2 p-0" aria-hidden="true">
          {items.map((o) => (
            <li key={o.key} class="flex items-center gap-2 text-[13.5px]">
              <span class={`ps-swatch ${FILL[o.tone]}`} />
              <span class="text-fg">{o.label}</span>
              <span class="font-mono text-muted">
                {formatCount(o.value)} <span class="text-faint">({formatPercent(o.value / total)})</span>
              </span>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
