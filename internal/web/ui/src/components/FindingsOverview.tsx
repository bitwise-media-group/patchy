// The top of the findings page: a sentence saying how much is in progress
// and what needs a person (each clause filters the list to it), then the
// lifecycle rail: every phase with its live count, each a filter toggle.
// The rail is eight cells on a wide screen and four by two on a phone.
// Every count is over all findings, whatever the filters: the server's
// facets, not the loaded rows.

import type { ComponentChildren } from "preact";
import type { FindingFacets, Phase } from "../types";
import { PHASE_ORDER } from "../types";
import { PHASE_TITLES, formatCount, plural } from "../format";

const CLOSED: Phase[] = ["Failed", "Dismissed", "HandedOff"];

const CLOSED_TONE: Record<string, string> = {
  Failed: "bg-red",
  Dismissed: "bg-soil",
  HandedOff: "bg-amber",
};


export function FindingsOverview({
  facets,
  selected,
  onToggle,
  onOnly,
}: {
  facets: FindingFacets | null;
  selected: Set<Phase>;
  onToggle: (phase: Phase) => void;
  // Replace the phase filter with exactly this phase.
  onOnly: (phase: Phase) => void;
}) {
  const count = (phase: Phase) => facets?.phases?.[phase] ?? 0;
  const total = facets?.total ?? 0;
  const open = facets?.open ?? 0;
  const running = facets?.running ?? 0;
  const suspended = facets?.suspended ?? 0;
  const awaiting = count("AwaitingApproval");
  const failed = count("Failed");

  const clause = (n: number, text: string, phase: Phase) => (
    <button
      type="button"
      class="ps-clause"
      onClick={() => onOnly(phase)}
    >
      {formatCount(n)} {text}
    </button>
  );

  const attention: ComponentChildren[] = [];
  if (awaiting) attention.push(clause(awaiting, awaiting === 1 ? "is waiting for approval" : "are waiting for approval", "AwaitingApproval"));
  if (failed) attention.push(clause(failed, failed === 1 ? "has failed" : "have failed", "Failed"));

  return (
    <section aria-labelledby="findings-headline">
      <h1 class="ps-title">Findings</h1>
      {total === 0 ? (
        <p id="findings-headline" class="mt-2 mb-0 max-w-[60ch] text-[15px] text-muted">
          No findings yet. They appear here when a scanner reports an alert to one of your integrations.
        </p>
      ) : (
        <>
          <p id="findings-headline" class="mt-3 mb-0 max-w-[48ch] text-[22px] leading-snug font-bold text-balance text-fg max-sm:text-[19px]">
            {plural(open, "finding", "findings")} in progress.{" "}
            {attention.length > 0 ? (
              <span>
                {attention.length === 2 ? (
                  <>
                    {attention[0]} and {attention[1]}.
                  </>
                ) : (
                  <>{attention[0]}.</>
                )}
              </span>
            ) : (
              <span class="text-muted">Nothing needs a person right now.</span>
            )}
          </p>
          {running > 0 ? (
            <p class="mt-2 mb-0 flex items-center gap-2 text-[14px] text-ink">
              <span class="ps-live-dot" aria-hidden="true" />
              {running === 1 ? "An agent is working on one of them now." : `Agents are working on ${formatCount(running)} of them now.`}
            </p>
          ) : null}
        </>
      )}

      <div class="mt-5 ps-card p-2">
        <ol class="m-0 grid list-none grid-cols-8 gap-1 p-0 max-lg:grid-cols-4" aria-label="Phases, in order. Select one to filter the list.">
          {PHASE_ORDER.map((phase) => {
            const n = count(phase);
            const on = selected.has(phase);
            return (
              <li key={phase} class="min-w-0">
                <button
                  type="button"
                  aria-pressed={on}
                  onClick={() => onToggle(phase)}
                  class={`flex h-full w-full cursor-pointer flex-col items-start gap-1.5 rounded-lg border-0 px-2.5 pt-2.5 pb-3 text-left transition-colors ${
                    on
                      ? "bg-[color-mix(in_oklab,var(--patchy-green)_12%,transparent)] shadow-[inset_0_0_0_1px_color-mix(in_oklab,var(--patchy-green)_45%,transparent)]"
                      : "bg-transparent hover:bg-surface-2"
                  }`}
                >
                  <span class={`block h-1 w-full rounded-full ${n ? (phase === "AwaitingApproval" ? "bg-amber" : "bg-turf") : "bg-line"}`} aria-hidden="true" />
                  <span class={`font-mono text-[22px] leading-none font-bold ${n ? "text-fg" : "text-faint"}`}>{formatCount(n)}</span>
                  <span class={`text-[13px] leading-tight ${n ? "text-fg" : "text-muted"}`}>{PHASE_TITLES[phase]}</span>
                </button>
              </li>
            );
          })}
        </ol>
        <div class="mt-1 flex flex-wrap items-center gap-x-1 gap-y-1 border-t border-line px-1.5 pt-2">
          <span class="mr-1.5 text-[13px] text-muted max-sm:w-full max-sm:px-1 max-sm:pb-0.5">Closed without a change</span>
          {CLOSED.map((phase) => {
            const n = count(phase);
            const on = selected.has(phase);
            return (
              <button
                key={phase}
                type="button"
                aria-pressed={on}
                onClick={() => onToggle(phase)}
                class={`inline-flex min-h-8 cursor-pointer items-center gap-2 rounded-md border-0 px-2.5 text-[13px] ${
                  on
                    ? "bg-[color-mix(in_oklab,var(--patchy-green)_12%,transparent)] text-fg shadow-[inset_0_0_0_1px_color-mix(in_oklab,var(--patchy-green)_45%,transparent)]"
                    : "bg-transparent text-fg hover:bg-surface-2"
                }`}
              >
                <span class={`size-2 rounded-full ${n ? CLOSED_TONE[phase] : "bg-line-2"}`} aria-hidden="true" />
                {PHASE_TITLES[phase]}
                <span class="font-mono text-muted">{formatCount(n)}</span>
              </button>
            );
          })}
          {suspended > 0 ? (
            <span class="ml-auto px-1.5 text-[13px] text-amber">{plural(suspended, "finding", "findings")} suspended</span>
          ) : null}
        </div>
      </div>
    </section>
  );
}
