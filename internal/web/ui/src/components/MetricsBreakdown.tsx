// Breakdown tables under the summary: repositories and the models and
// harnesses the agents ran on. Every name links to that slice of the
// metrics.
//
// Repositories can number tens of thousands, so their table never holds
// them all: sorting and paging happen server-side
// (GET /api/rollups/repositories), the table fetches just the rows it
// shows, and the filter above finds the rest.

import { useEffect, useState } from "preact/hooks";
import type { RepositorySort, Rollup } from "../types";
import { DASH, formatCount, formatMicroUSD, formatMs, formatPercent } from "../format";
import { queryRepositories } from "../api";
import {
  REPO_SORT_LABELS,
  costPerRemediation,
  failedCount,
  remediatedShare,
  stageTotals,
  type SliceKind,
} from "../rollups";

type RepoSort = Exclude<RepositorySort, "relevance">;
import { hrefForMetrics } from "../router";
import { Icon } from "./icons";
import { OutcomeStrip, phaseOutcomes } from "./OutcomeStrip";

const PAGE = 10;
const MORE = 25;

const SECTION = "overflow-hidden ps-card";

function SortHeader({
  by,
  sort,
  descending,
  onSort,
  numeric = true,
}: {
  by: RepoSort;
  sort: RepoSort;
  descending: boolean;
  onSort: (by: RepoSort) => void;
  numeric?: boolean;
}) {
  const active = by === sort;
  return (
    <th
      scope="col"
      class={numeric ? "text-right" : ""}
      aria-sort={active ? (descending ? "descending" : "ascending") : undefined}
    >
      <button type="button" class="ps-sort" data-active={active || undefined} onClick={() => onSort(by)}>
        {REPO_SORT_LABELS[by]}
        <Icon
          name={active ? "chevronDown" : "arrowUpDown"}
          size={12}
          class={active ? (descending ? "" : "rotate-180") : "opacity-50"}
        />
      </button>
    </th>
  );
}

// SERVER_PAGE is the server's per-request cap; longer tables fetch in
// chunks.
const SERVER_PAGE = 100;

export function RepositoryTable({
  q,
  title,
  caption,
  refresh,
}: {
  // Restrict to repositories whose name contains q (a "match" slice).
  q?: string;
  title: string;
  caption?: string;
  // Bumped when the data changed server-side; the shown rows refetch.
  refresh: number;
}) {
  const [sort, setSort] = useState<RepoSort>("findings");
  const [descending, setDescending] = useState(true);
  const [shown, setShown] = useState(PAGE);
  const [rows, setRows] = useState<Rollup[]>([]);
  const [total, setTotal] = useState<number | null>(null);
  const [failed, setFailed] = useState(false);

  // Fetch exactly the rows on show, in server-sized chunks. Previous rows
  // stay up until the answer lands, so sorting never flashes empty.
  useEffect(() => {
    const ctl = new AbortController();
    void (async () => {
      try {
        const out: Rollup[] = [];
        let count = 0;
        for (let offset = 0; offset < shown; offset += SERVER_PAGE) {
          const page = await queryRepositories(
            { q, sort, order: descending ? "desc" : "asc", offset, limit: Math.min(SERVER_PAGE, shown - offset) },
            ctl.signal,
          );
          out.push(...page.repositories);
          count = page.total;
          if (out.length >= count) break;
        }
        setRows(out);
        setTotal(count);
        setFailed(false);
      } catch {
        if (!ctl.signal.aborted) setFailed(true);
      }
    })();
    return () => ctl.abort();
  }, [q, sort, descending, shown, refresh]);

  const totalCount = total ?? 0;

  const onSort = (by: RepoSort) => {
    if (by === sort) setDescending(!descending);
    else {
      setSort(by);
      setDescending(by !== "name");
    }
    setShown(PAGE);
  };

  const head = { sort, descending, onSort };

  return (
    <section class={SECTION} aria-labelledby="metrics-repos">
      <header class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 px-5 pt-4.5 pb-3.5">
        <h2 id="metrics-repos" class="ps-heading">
          {title}{" "}
          <span class="font-mono text-[13px] font-normal text-muted">{total === null ? "" : formatCount(totalCount)}</span>
        </h2>
        {caption ? <p class="m-0 text-[13px] text-muted">{caption}</p> : null}
      </header>
      <div class="overflow-x-auto">
        <table class="ps-table min-w-[860px]">
          <thead>
            <tr>
              <SortHeader by="name" numeric={false} {...head} />
              <SortHeader by="findings" {...head} />
              <th scope="col" class="w-[22%]">
                Outcomes
              </th>
              <SortHeader by="remediated" {...head} />
              <SortHeader by="failed" {...head} />
              <SortHeader by="spend" {...head} />
              <SortHeader by="costPerRemediation" {...head} />
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => {
              const key = r.scope.key ?? "";
              const b = r.bucket;
              const cpr = costPerRemediation(b);
              const spend = stageTotals(b).costMicroUSD;
              return (
                <tr key={key}>
                  <th scope="row" class="font-normal">
                    <a
                      class="block max-w-[340px] truncate font-mono text-[13px] text-fg no-underline hover:text-ink hover:underline max-sm:max-w-[220px]"
                      href={hrefForMetrics({ kind: "repository", key })}
                      title={key}
                    >
                      {key}
                    </a>
                  </th>
                  <td class="num">{formatCount(b.findings)}</td>
                  <td>
                    <OutcomeStrip items={phaseOutcomes(b.phases ?? {})} label={`${key} findings by outcome`} thin legend={false} />
                  </td>
                  <td class="num">{formatPercent(remediatedShare(b))}</td>
                  <td class="num">{formatCount(failedCount(b))}</td>
                  <td class="num">{spend ? formatMicroUSD(spend) : DASH}</td>
                  <td class="num">{cpr !== undefined ? formatMicroUSD(cpr) : DASH}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {failed ? (
        <p class="m-0 border-t border-line px-5 py-3 text-[13px] text-red">
          Couldn't load repositories. They'll load again when metrics next change, or reload the page.
        </p>
      ) : null}
      {totalCount > PAGE ? (
        <footer class="flex flex-wrap items-center justify-between gap-3 border-t border-line px-5 py-3 text-[13px] text-muted">
          <span aria-live="polite">
            Showing {formatCount(rows.length)} of {formatCount(totalCount)}
            {totalCount > shown ? ". Use the filter above to find a specific repository." : "."}
          </span>
          <span class="flex gap-2">
            {shown > PAGE ? (
              <button type="button" class="ps-action min-h-8" onClick={() => setShown(PAGE)}>
                Show fewer
              </button>
            ) : null}
            {totalCount > shown ? (
              <button type="button" class="ps-action min-h-8" onClick={() => setShown(shown + MORE)}>
                Show {formatCount(Math.min(MORE, totalCount - shown))} more
              </button>
            ) : null}
          </span>
        </footer>
      ) : null}
    </section>
  );
}

export function AgentTable({ kind, title, rollups }: { kind: SliceKind; title: string; rollups: Rollup[] }) {
  if (rollups.length === 0) return null;
  const rows = rollups
    .map((r) => ({ r, t: stageTotals(r.bucket) }))
    .sort((a, b) => b.t.runs - a.t.runs);
  const maxSpend = Math.max(...rows.map((x) => x.t.costMicroUSD), 1);
  const id = `metrics-${kind}`;
  return (
    <section class={SECTION} aria-labelledby={id}>
      <header class="px-5 pt-4.5 pb-3.5">
        <h2 id={id} class="ps-heading">
          {title}
        </h2>
      </header>
      <div class="overflow-x-auto">
        <table class="ps-table min-w-[560px]">
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col" class="text-right">
                Runs
              </th>
              <th scope="col" class="text-right">
                Succeeded
              </th>
              <th scope="col" class="text-right">
                Per run
              </th>
              <th scope="col" class="text-right">
                Run time
              </th>
              <th scope="col" class="text-right">
                Spend
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map(({ r, t }) => {
              const key = r.scope.key ?? "";
              return (
                <tr key={key}>
                  <th scope="row" class="font-normal whitespace-nowrap">
                    <a
                      class="font-mono text-[13px] text-fg no-underline hover:text-ink hover:underline"
                      href={hrefForMetrics({ kind, key })}
                    >
                      {key}
                    </a>
                  </th>
                  <td class="num">{formatCount(t.runs)}</td>
                  <td class="num">{formatPercent(t.runs ? t.succeeded / t.runs : undefined)}</td>
                  <td class="num">{t.runs ? formatMicroUSD(t.costMicroUSD / t.runs) : DASH}</td>
                  <td class="num">{formatMs(t.runs ? t.elapsedMilliseconds / t.runs : undefined)}</td>
                  <td class="whitespace-nowrap">
                    <span class="flex items-center justify-end gap-3">
                      <span class="block h-1.5 w-12 flex-none overflow-hidden rounded-full bg-code" aria-hidden="true">
                        <span class="block h-full rounded-full bg-turf" style={{ width: `${(t.costMicroUSD / maxSpend) * 100}%` }} />
                      </span>
                      <span class="min-w-[5.5rem] text-right font-mono">
                        {t.costMicroUSD ? formatMicroUSD(t.costMicroUSD) : DASH}
                      </span>
                    </span>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
