// The metrics page: all-time statistics from the rollup shards, filterable
// to one repository, a set of matching repositories, one model or one
// harness. Gated like findings, on its own grant (get on findingrollups).
//
// It loads its own data rather than riding on the findings payload: the
// summary (GET /api/rollups: total, models, harnesses, top repositories)
// once, then a repository or "matching" slice on demand from
// GET /api/rollups/repositories. A findings-changed signal refetches what is
// on screen.

import { useCallback, useEffect, useMemo, useState } from "preact/hooks";
import type { Dataset, Rollup } from "../types";
import { formatAgo, formatDay } from "../format";
import { ForbiddenError, fetchRollups, queryRepositories, subscribe } from "../api";
import type { RollupSlice } from "../rollups";
import { hrefForMetrics, navigate } from "../router";
import { AgentTable, RepositoryTable } from "./MetricsBreakdown";
import { MetricsFilter, SLICE_LABELS } from "./MetricsFilter";
import { MetricsSummary } from "./MetricsSummary";

interface Resolved {
  rollup?: Rollup;
  rank?: number;
  members?: number;
}

const byType = (ds: Dataset | null, type: string) => (ds?.rollups ?? []).filter((r) => r.scope.type === type);

export function MetricsView({ slice }: { slice: RollupSlice | null }) {
  const [summary, setSummary] = useState<Dataset | null>(null);
  const [error, setError] = useState<{ denied: boolean; message: string } | null>(null);
  // Bumped by the change signal; everything on screen refetches.
  const [refresh, setRefresh] = useState(0);

  useEffect(() => subscribe(() => setRefresh((n) => n + 1)), []);
  useEffect(() => {
    fetchRollups()
      .then((ds) => {
        setSummary(ds);
        setError(null);
      })
      .catch((e: unknown) =>
        setError({ denied: e instanceof ForbiddenError, message: e instanceof Error ? e.message : String(e) }),
      );
  }, [refresh]);

  const total = useMemo(() => summary?.rollups?.find((r) => r.scope.type === "total"), [summary]);
  const models = useMemo(() => byType(summary, "model"), [summary]);
  const harnesses = useMemo(() => byType(summary, "harness"), [summary]);
  const topRepositories = useMemo(() => byType(summary, "repository"), [summary]);
  const repositoryCount = summary?.repositoryCount ?? topRepositories.length;

  // Repository and "matching" slices are asked of the server; model and
  // harness slices come with the summary. sliceKey records what was
  // resolved, so a stale answer never renders against a newer slice.
  const sliceKey = slice ? `${slice.kind}:${slice.key}` : "";
  const [resolved, setResolved] = useState<{ key: string; value: Resolved } | null>(null);
  useEffect(() => {
    if (!slice || (slice.kind !== "repository" && slice.kind !== "match")) return;
    const ctl = new AbortController();
    const q =
      slice.kind === "repository" ? { key: slice.key, limit: 1 } : { q: slice.key, aggregate: true, limit: 0 };
    queryRepositories(q, ctl.signal)
      .then((page) =>
        setResolved({
          key: sliceKey,
          value:
            slice.kind === "repository"
              ? { rollup: page.repositories[0], rank: page.rank }
              : { rollup: page.aggregate, members: page.total },
        }),
      )
      .catch(() => {
        if (!ctl.signal.aborted) setResolved({ key: sliceKey, value: {} });
      });
    return () => ctl.abort();
    // slice is captured through sliceKey.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sliceKey, refresh]);

  let current: Resolved | undefined;
  if (!slice) current = { rollup: total };
  else if (slice.kind === "model") current = { rollup: models.find((r) => r.scope.key === slice.key) };
  else if (slice.kind === "harness") current = { rollup: harnesses.find((r) => r.scope.key === slice.key) };
  else if (resolved?.key === sliceKey) current = resolved.value;
  const loading = !summary || current === undefined;

  const onSelect = useCallback((s: RollupSlice | null) => navigate(hrefForMetrics(s)), []);
  const epoch = total ?? current?.rollup;

  let body;
  if (error && !summary) {
    body = (
      <div class="rounded-[10px] border border-line px-6 py-10 text-center">
        <p class="m-0 text-[16px] font-bold text-fg">{error.denied ? "Permission denied" : "Couldn't load metrics"}</p>
        <p class="mx-auto mt-2 mb-0 max-w-[60ch] text-muted">
          {error.denied ? `${error.message} Metrics need get on findingrollups.` : error.message}
        </p>
      </div>
    );
  } else if (loading) {
    body = <div class="px-5 py-11 text-center text-muted">Loading…</div>;
  } else if (!current?.rollup) {
    body = (
      <div class="rounded-[10px] border border-dashed border-line-2 px-6 py-12 text-center">
        <p class="m-0 text-[16px] font-bold text-fg">
          {slice ? `No metrics for ${SLICE_LABELS[slice.kind].toLowerCase()} “${slice.key}” yet` : "No metrics yet"}
        </p>
        <p class="mx-auto mt-2 mb-0 max-w-[52ch] text-muted">
          Metrics are recorded when a finding completes, then kept after the finding itself expires.
        </p>
        {slice ? (
          <a class="ps-action mt-5 no-underline" href={hrefForMetrics()}>
            Show all metrics
          </a>
        ) : null}
      </div>
    );
  } else {
    body = (
      <>
        <MetricsSummary
          total={total}
          repositoryCount={repositoryCount}
          rank={current.rank}
          slice={slice}
          rollup={current.rollup}
          members={current.members}
        />
        {slice === null ? (
          <div class="mt-4 flex flex-col gap-4">
            <RepositoryTable title="Repositories" refresh={refresh} />
            <div class="grid grid-cols-2 gap-4 max-xl:grid-cols-1">
              <AgentTable kind="model" title="Models" rollups={models} />
              <AgentTable kind="harness" title="Harnesses" rollups={harnesses} />
            </div>
          </div>
        ) : slice.kind === "match" ? (
          <div class="mt-4">
            <RepositoryTable key={slice.key} q={slice.key} title="Matching repositories" refresh={refresh} />
          </div>
        ) : null}
      </>
    );
  }

  return (
    <div>
      <header class="mb-5 flex flex-wrap items-end justify-between gap-x-8 gap-y-4">
        <div>
          <h1 class="m-0 text-[28px] leading-tight font-bold tracking-[-0.015em]">Metrics</h1>
          <p class="mt-1 mb-0 text-[14px] text-muted">
            {epoch?.firstProcessed ? (
              <>
                All-time, since {formatDay(epoch.firstProcessed)}.
                {epoch.lastProcessed ? ` Updated ${formatAgo(epoch.lastProcessed)}.` : ""}
              </>
            ) : (
              "All-time statistics for completed findings."
            )}
          </p>
        </div>
        <div class="w-[min(460px,100%)]">
          <MetricsFilter
            models={models}
            harnesses={harnesses}
            topRepositories={topRepositories}
            repositoryCount={repositoryCount}
            slice={slice}
            onSelect={onSelect}
          />
        </div>
      </header>
      {body}
      <p class="mt-6 mb-0 max-w-[80ch] text-[13px] text-faint">
        Metrics outlive the findings they count: they are kept when findings expire, and start again only if the
        cluster is rebuilt.
      </p>
    </div>
  );
}
