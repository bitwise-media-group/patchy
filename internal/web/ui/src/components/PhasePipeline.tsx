// The lifecycle rail: the eight-phase forward path plus the three other
// terminals, each with a live count. Clicking a phase toggles it as a
// filter.

import type { Finding, Phase } from "../types";
import { PHASE_ORDER } from "../types";
import { PHASE_LABELS } from "../format";
import { phaseCounts } from "../filters";

const OTHER_TERMINALS: Phase[] = ["Failed", "Dismissed", "HandedOff"];

export function PhasePipeline({
  findings,
  selected,
  onToggle,
}: {
  findings: Finding[];
  selected: Set<Phase>;
  onToggle: (phase: Phase) => void;
}) {
  const { counts, suspended } = phaseCounts(findings);

  const cell = (phase: Phase, rail: boolean, index?: number) => {
    const count = counts.get(phase) ?? 0;
    const active = selected.has(phase);
    return (
      <button
        key={phase}
        type="button"
        class={`ps-phase ${rail ? "" : "ps-phase--terminal"} ${active ? "is-active" : ""} ${count === 0 ? "is-empty" : ""}`}
        aria-pressed={active}
        onClick={() => onToggle(phase)}
        title={`${PHASE_LABELS[phase]}: ${count} finding${count === 1 ? "" : "s"}`}
      >
        <span class="ps-phase__top">
          <span class="ps-phase__count">{count}</span>
          {rail && index !== undefined && index < PHASE_ORDER.length - 1 ? (
            <span class={`ps-phase__line ${(index + 1) % 4 === 0 ? "max-lg:hidden" : ""}`} />
          ) : null}
        </span>
        <strong>{PHASE_LABELS[phase]}</strong>
      </button>
    );
  };

  return (
    <section class="mt-4.5 rounded-xl border border-line bg-surface-2 px-5 pt-4.5 pb-3.5" aria-label="Lifecycle phases">
      <div class="grid grid-cols-8 max-lg:grid-cols-4 max-lg:gap-y-4" role="group" aria-label="Forward path">
        {PHASE_ORDER.map((phase, i) => cell(phase, true, i))}
      </div>
      <div class="mt-3 flex items-center gap-2.5 border-t border-dashed border-line pt-3" role="group" aria-label="Other terminal phases">
        {OTHER_TERMINALS.map((phase) => cell(phase, false))}
        {suspended > 0 ? (
          <span class="ml-auto font-mono text-[10.5px] text-amber" title={`${suspended} suspended`}>
            {suspended} suspended
          </span>
        ) : null}
      </div>
    </section>
  );
}
