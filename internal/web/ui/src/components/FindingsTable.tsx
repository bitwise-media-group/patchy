// The findings list. On wide screens a six-column table; on narrow ones
// each finding stacks (what it is, then its state) with nothing to scroll
// sideways. Rows are list items whose title is the link, stretched over the
// whole row, so the pull-request link inside stays a separate, valid link.
// Rows arrive a page at a time, and the next page loads as the end of the
// list scrolls near (see findingsList.ts).

import { useEffect, useRef } from "preact/hooks";
import type { FindingSummary } from "../types";
import { MAX_ROWS, type FindingsList } from "../findingsList";
import { confidencePercent, DASH, formatAgo, formatConfidence, formatCount } from "../format";
import { hrefForFinding } from "../router";
import { shortName } from "../rollups";
import { Icon } from "./icons";
import { PhasePill, SeverityPill, VerdictPill, Pill } from "./Pills";
import { PullRequestPill } from "./PullRequest";

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
              {/* The owner is the same on nearly every row, so the name alone
                  leads; the full name is the tooltip. */}
              <span class="truncate font-medium" title={finding.repository.name}>
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
          {finding.suspend ? <Pill tone="amber">Suspended</Pill> : null}
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

// LOAD_AHEAD_PX starts fetching the next page this far before the end of
// the list comes into view, so steady scrolling rarely waits.
const LOAD_AHEAD_PX = 800;

export function FindingsTable({
  list,
  total,
  filtered,
  onClear,
}: {
  list: FindingsList;
  // How many findings exist at all, filters aside, for the count line.
  total: number;
  filtered: boolean;
  onClear?: () => void;
}) {
  const section = useRef<HTMLElement>(null);
  const sentinel = useRef<HTMLDivElement>(null);
  const loadMore = useRef(list.loadMore);
  loadMore.current = list.loadMore;

  // Load the next page as the end of the list nears. Re-observing whenever
  // the rows change (a page appended, or a new selection's rows swapped in)
  // makes the observer report again when the end is still in view, which it
  // would not do on its own: it reports changes, not states.
  const { hasMore, rows } = list;
  useEffect(() => {
    const el = sentinel.current;
    if (!el || !hasMore) return;
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) loadMore.current();
    }, { rootMargin: `${LOAD_AHEAD_PX}px 0px` });
    io.observe(el);
    return () => io.disconnect();
  }, [hasMore, rows]);

  const showLatest = () => {
    list.showPending();
    const top = section.current?.getBoundingClientRect().top ?? 0;
    if (top < 0) section.current?.scrollIntoView({ block: "start" });
  };

  const capped = !list.hasMore && rows.length >= MAX_ROWS && list.matched > rows.length;
  return (
    <section
      ref={section}
      class={`mt-4 overflow-hidden ps-card transition-opacity ${list.switching ? "opacity-60" : ""}`}
      aria-labelledby="findings-count"
      aria-busy={list.switching || list.loadingMore}
    >
      <header class="flex flex-wrap items-baseline justify-between gap-2 border-b border-line px-5 py-3">
        <h2 id="findings-count" class="m-0 text-[14px] font-semibold text-fg" aria-live="polite">
          {filtered ? `${formatCount(list.matched)} of ${formatCount(total)} findings` : `${formatCount(total)} findings`}
        </h2>
        {list.pending ? (
          <button
            type="button"
            class="inline-flex cursor-pointer items-center gap-2 rounded-full border border-line-3 bg-surface px-3 py-1 text-[13px] font-semibold text-ink hover:bg-surface-2"
            onClick={showLatest}
          >
            <span class="ps-live-dot" aria-hidden="true" />
            Updated. Show the latest
          </button>
        ) : (
          <span class="text-[13px] text-muted">Most severe first</span>
        )}
      </header>
      <div class={`${COLS} hidden min-h-10 border-b border-line bg-surface-2 px-5 text-[12.5px] font-semibold text-muted lg:grid`} aria-hidden="true">
        <span>Finding</span>
        <span>Severity</span>
        <span>Status</span>
        <span>Confidence</span>
        <span>Verdict</span>
        <span>First seen</span>
      </div>
      {rows.length === 0 ? (
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
        <>
          <ul class="m-0 list-none p-0">
            {rows.map((f) => (
              <FindingRow key={f.name} finding={f} />
            ))}
          </ul>
          {/* Always laid out: an observer never reports a display:none target. */}
          <div ref={sentinel} class="h-px" aria-hidden="true" />
          <div class="border-t border-line px-5 py-3 text-center text-[13px] text-muted empty:hidden">
            {list.moreFailed ? (
              <>
                Could not load more findings.{" "}
                <button type="button" class="cursor-pointer border-0 bg-transparent p-0 font-semibold text-ink underline underline-offset-2" onClick={list.loadMore}>
                  Try again
                </button>
              </>
            ) : list.loadingMore ? (
              "Loading more…"
            ) : capped ? (
              `Showing the first ${formatCount(rows.length)} of ${formatCount(list.matched)}. Narrow the filters to see the rest.`
            ) : null}
          </div>
        </>
      )}
    </section>
  );
}
