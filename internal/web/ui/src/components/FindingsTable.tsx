// The findings list. On wide screens a six-column table; on narrow ones
// each finding stacks — what it is, then its state — with nothing to scroll
// sideways. Rows are list items whose title is the link, stretched over the
// whole row, so the pull-request link inside stays a separate, valid link.

import type { FindingSummary } from "../types";
import { confidencePercent, DASH, formatAgo, formatConfidence, formatCount } from "../format";
import { hrefForFinding } from "../router";
import { shortName } from "../rollups";
import { Icon } from "./icons";
import { PhasePill, SeverityPill, VerdictPill, Pill } from "./Pills";
import { PullRequestPill } from "./PullRequest";

const owner = (key: string) => key.slice(0, key.lastIndexOf("/") + 1);

const COLS = "lg:grid lg:grid-cols-[minmax(0,2.6fr)_0.8fr_1.2fr_0.8fr_0.8fr_0.7fr] lg:items-center lg:gap-4";

function FindingRow({ finding }: { finding: FindingSummary }) {
  const advisory = finding.advisories[0] ?? finding.ruleID ?? finding.name;
  const confidence = finding.investigation?.confidence;
  return (
    <li
      class={`${COLS} relative border-b border-line px-5 py-3.5 last:border-b-0 hover:bg-[color-mix(in_oklab,var(--patchy-green)_4%,var(--patchy-surface))]`}
    >
      <div class="min-w-0">
        <span class="flex min-w-0 items-center gap-1.5 text-[12.5px] text-muted">
          {finding.repository?.name ? (
            <>
              <Icon name="github" size={13} class="flex-none" />
              <span class="truncate font-mono" title={finding.repository.name}>
                {/* Phones drop the owner: in one organisation it is the same on every row. */}
                <span class="max-sm:hidden">{owner(finding.repository.name)}</span>
                {shortName(finding.repository.name)}
              </span>
              <span class="flex-none text-faint" aria-hidden="true">
                /
              </span>
            </>
          ) : null}
          <span class="flex-none font-mono">{advisory}</span>
        </span>
        <a
          href={hrefForFinding(finding.name)}
          class="mt-1 block text-[14.5px] leading-snug font-semibold text-fg no-underline after:absolute after:inset-0 after:content-[''] hover:underline focus-visible:outline-none focus-visible:after:outline-2 focus-visible:after:-outline-offset-2 focus-visible:after:outline-[var(--focus-ring)]"
        >
          {finding.title ?? finding.name}
        </a>
      </div>

      <div class="mt-2.5 flex flex-wrap items-center gap-x-2 gap-y-1.5 lg:contents">
        <div>
          <SeverityPill severity={finding.severity} />
        </div>
        <div class="flex flex-wrap items-center gap-1.5 lg:flex-col lg:items-start lg:gap-1">
          {finding.phase === "InReview" && finding.pullRequest?.url ? (
            <span class="relative z-[1]">
              <PullRequestPill pr={finding.pullRequest} />
            </span>
          ) : (
            <PhasePill phase={finding.phase} />
          )}
          {finding.suspend ? <Pill tone="amber">suspended</Pill> : null}
          {finding.activeRun ? (
            <span class="inline-flex items-center gap-1.5 text-[12.5px] text-ink" title={`${finding.activeRun.kind} running`}>
              <span class="ps-live-dot" /> {finding.activeRun.kind}
            </span>
          ) : null}
        </div>
        <div class="max-lg:order-last max-lg:w-full">
          {confidence ? (
            <span class="flex items-center gap-2 lg:block">
              <span class="font-mono text-[12.5px] text-fg">
                {formatConfidence(confidence)}
                <span class="text-muted lg:hidden"> confidence</span>
              </span>
              <span class="ps-track max-lg:mt-0">
                <span style={{ width: `${confidencePercent(confidence)}%` }} />
              </span>
            </span>
          ) : (
            <span class="font-mono text-[12.5px] text-faint max-lg:hidden">{DASH}</span>
          )}
        </div>
        <div class={finding.investigation?.recommendation ? "" : "max-lg:hidden"}>
          <VerdictPill verdict={finding.investigation?.recommendation} />
        </div>
        <div class="text-[12.5px] text-muted max-lg:ml-auto">
          <span class="lg:hidden">First seen </span>
          {formatAgo(finding.firstObservedAt)}
        </div>
      </div>
    </li>
  );
}

export function FindingsTable({
  findings,
  total,
  onClear,
}: {
  findings: FindingSummary[];
  // How many there are before filtering, for the count line.
  total: number;
  onClear?: () => void;
}) {
  const filtered = findings.length !== total;
  return (
    <section class="mt-4 overflow-hidden rounded-[10px] border border-line bg-surface" aria-labelledby="findings-count">
      <header class="flex flex-wrap items-baseline justify-between gap-2 border-b border-line px-5 py-3">
        <h2 id="findings-count" class="m-0 text-[14px] font-semibold text-fg" aria-live="polite">
          {filtered ? `${formatCount(findings.length)} of ${formatCount(total)} findings` : `${formatCount(total)} findings`}
        </h2>
        <span class="text-[13px] text-muted">Most severe first</span>
      </header>
      <div class={`${COLS} hidden min-h-10 border-b border-line bg-surface-2 px-5 text-[12.5px] font-semibold text-muted lg:grid`} aria-hidden="true">
        <span>Finding</span>
        <span>Severity</span>
        <span>Status</span>
        <span>Confidence</span>
        <span>Verdict</span>
        <span>First seen</span>
      </div>
      {findings.length === 0 ? (
        <div class="px-5 py-10 text-center text-[14px] text-muted">
          {total === 0 ? (
            "Nothing here yet."
          ) : (
            <>
              No findings match these filters.{" "}
              {onClear ? (
                <button type="button" class="cursor-pointer border-0 bg-transparent p-0 font-semibold text-ink underline underline-offset-2" onClick={onClear}>
                  Clear filters
                </button>
              ) : null}
            </>
          )}
        </div>
      ) : (
        <ul class="m-0 list-none p-0">
          {findings.map((f) => (
            <FindingRow key={f.name} finding={f} />
          ))}
        </ul>
      )}
    </section>
  );
}
