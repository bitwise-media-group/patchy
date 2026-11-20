// The metrics filter: the shared FilterBox over every slice the rollups can
// answer (a repository, a model, a harness, or every repository matching
// the typed term, summed). Models and harnesses are few and come with the
// summary; repositories can number tens of thousands, so their matches are
// asked of the server as you type (debounced, stale requests cancelled) and
// only the best few come back with a count of the rest.

import { useCallback, useEffect, useMemo, useState } from "preact/hooks";
import type { RepositoryPage, Rollup } from "../types";
import { formatCount } from "../format";
import { queryRepositories } from "../api";
import { stageTotals, type RollupSlice } from "../rollups";
import { locate, rank } from "../typeahead";
import { FilterBox, Highlight, type FilterGroup, type FilterOption } from "./FilterBox";

export const SLICE_LABELS: Record<RollupSlice["kind"], string> = {
  repository: "Repository",
  model: "Model",
  harness: "Harness",
  match: "Repositories matching",
};

const REPO_SUGGESTIONS = 8;
const AGENT_SUGGESTIONS = 5;
const DEBOUNCE_MS = 120;

function figure(r: Rollup): string {
  const b = r.bucket;
  if (b.findings !== undefined) return `${formatCount(b.findings)} findings`;
  return `${formatCount(stageTotals(b).runs)} runs`;
}

const runsOf = (r: Rollup) => stageTotals(r.bucket).runs;

export function MetricsFilter({
  models,
  harnesses,
  topRepositories,
  repositoryCount,
  slice,
  onSelect,
}: {
  models: Rollup[];
  harnesses: Rollup[];
  // The summary's top repositories, shown before anything is typed.
  topRepositories: Rollup[];
  repositoryCount: number;
  slice: RollupSlice | null;
  onSelect: (slice: RollupSlice | null) => void;
}) {
  const [term, setTerm] = useState("");
  const [remote, setRemote] = useState<{ term: string; page: RepositoryPage } | null>(null);

  useEffect(() => {
    if (term === "") return;
    const ctl = new AbortController();
    const timer = setTimeout(() => {
      queryRepositories({ q: term, limit: REPO_SUGGESTIONS }, ctl.signal)
        .then((page) => setRemote({ term, page }))
        .catch(() => {
          // Aborted (superseded) or failed: the next keystroke retries.
        });
    }, DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      ctl.abort();
    };
  }, [term]);

  const modelKeys = useMemo(() => models.map((r) => (r.scope.key ?? "").toLowerCase()), [models]);
  const harnessKeys = useMemo(() => harnesses.map((r) => (r.scope.key ?? "").toLowerCase()), [harnesses]);

  const suggest = useCallback(
    (t: string): FilterGroup[] => {
      const needle = t.toLowerCase();
      const option = (kind: RollupSlice["kind"], r: Rollup, at: number): FilterOption => {
        const key = r.scope.key ?? "";
        return {
          key: `${kind}:${key}`,
          content: (
            <span class="font-mono">
              <Highlight text={key} at={at} len={t.length} />
            </span>
          ),
          aside: figure(r),
          selected: slice?.kind === kind && slice.key === key,
          onPick: () => onSelect({ kind, key }),
        };
      };

      const groups: FilterGroup[] = [];
      // Matches for this exact term, or the last answer while the next one
      // is in flight (so the list doesn't flash empty between keystrokes).
      const answer = t === "" ? null : remote;
      const fresh = answer?.term === t;
      if (t !== "" && fresh && answer.page.total > 1) {
        groups.push({
          options: [
            {
              key: "match",
              content: (
                <span>
                  All {formatCount(answer.page.total)} repositories matching <span class="font-mono">“{t}”</span>
                </span>
              ),
              aside: "combined",
              onPick: () => onSelect({ kind: "match", key: t }),
            },
          ],
        });
      }
      if (t === "") {
        groups.push({
          label: "Top repositories",
          note: `of ${formatCount(repositoryCount)}`,
          options: topRepositories.slice(0, REPO_SUGGESTIONS).map((r) => option("repository", r, -1)),
        });
      } else if (answer) {
        const shown = answer.page.repositories;
        const more = answer.page.total - shown.length;
        groups.push({
          label: "Repositories",
          note: fresh ? `${formatCount(answer.page.total)} found` : "Searching…",
          options: shown.map((r) => option("repository", r, locate((r.scope.key ?? "").toLowerCase(), needle)[0])),
          footer: fresh && more > 0 ? `${formatCount(more)} more. Keep typing to narrow the list.` : undefined,
        });
      }
      const [m] = rank(models, modelKeys, needle, AGENT_SUGGESTIONS, runsOf);
      const [h] = rank(harnesses, harnessKeys, needle, AGENT_SUGGESTIONS, runsOf);
      groups.push({ label: "Models", options: m.map(({ item, at }) => option("model", item, at)) });
      groups.push({ label: "Harnesses", options: h.map(({ item, at }) => option("harness", item, at)) });
      return groups;
    },
    [remote, models, harnesses, modelKeys, harnessKeys, topRepositories, repositoryCount, slice, onSelect],
  );

  return (
    <FilterBox
      label="Filter metrics by repository, model or harness"
      placeholder="Filter by repository, model or harness"
      chips={
        slice
          ? [
              {
                id: "slice",
                kind: SLICE_LABELS[slice.kind],
                value: slice.kind === "match" ? `“${slice.key}”` : slice.key,
                mono: true,
              },
            ]
          : []
      }
      onRemove={() => onSelect(null)}
      suggest={suggest}
      onQuery={setTerm}
      empty={(t) =>
        remote?.term === t
          ? `Nothing matches “${t}”. Repositories appear here once one of their findings completes.`
          : "Searching repositories…"
      }
      shortcut
    />
  );
}
