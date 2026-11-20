// The findings filter: the shared FilterBox (same control as the metrics
// page) over the findings selection. Typing offers a free-text search
// first, then whichever severities, phases, verdicts and repositories match
// what was typed; every applied filter is a chip in the field.

import { useCallback, useMemo } from "preact/hooks";
import type { FindingSummary, Level, Phase, Recommendation } from "../types";
import { PHASE_ORDER } from "../types";
import { PHASE_TITLES, SEVERITY_TITLES, VERDICT_TITLES, formatCount } from "../format";
import { filterFindings, type Selection } from "../filters";
import { rank } from "../typeahead";
import { FilterBox, Highlight, type FilterChip, type FilterGroup, type FilterOption } from "./FilterBox";

const SEVERITIES: Level[] = ["critical", "high", "medium", "low"];
const VERDICTS: Recommendation[] = ["remediate", "ignore", "manual"];
const PHASES: Phase[] = [...PHASE_ORDER, "Failed", "Dismissed", "HandedOff"];

function toggle<T>(set: Set<T>, v: T): Set<T> {
  const next = new Set(set);
  next.has(v) ? next.delete(v) : next.add(v);
  return next;
}

function tally<K extends string>(findings: FindingSummary[], key: (f: FindingSummary) => K | undefined): Map<K, number> {
  const m = new Map<K, number>();
  for (const f of findings) {
    const k = key(f);
    if (k) m.set(k, (m.get(k) ?? 0) + 1);
  }
  return m;
}

// matchLabel finds term in a vocabulary label, start-of-word first.
function matchLabel(label: string, term: string): number {
  return term === "" ? -1 : label.toLowerCase().indexOf(term.toLowerCase());
}

export function FindingsFilter({
  findings,
  repos,
  selection,
  onChange,
}: {
  findings: FindingSummary[];
  repos: string[];
  selection: Selection;
  onChange: (next: Selection) => void;
}) {
  const counts = useMemo(
    () => ({
      severity: tally(findings, (f) => f.severity),
      verdict: tally(findings, (f) => f.investigation?.recommendation),
      phase: tally(findings, (f) => f.phase),
      repo: tally(findings, (f) => f.repository?.name),
    }),
    [findings],
  );
  const repoKeys = useMemo(() => repos.map((r) => r.toLowerCase()), [repos]);

  const suggest = useCallback(
    (term: string): FilterGroup[] => {
      const needle = term.toLowerCase();
      const n = (m: Map<string, number>, k: string) => formatCount(m.get(k) ?? 0);
      const vocab = <K extends string>(
        values: K[],
        titles: Record<K, string>,
        tallyOf: Map<string, number>,
        selected: Set<K>,
        apply: (v: K) => Selection,
        always: boolean,
      ): FilterOption[] =>
        values
          .map((v) => ({ v, at: matchLabel(titles[v], term) }))
          .filter(({ at }) => (term === "" ? always : at >= 0))
          .map(({ v, at }) => ({
            key: v,
            content: <Highlight text={titles[v]} at={at} len={term.length} />,
            aside: n(tallyOf, v),
            selected: selected.has(v),
            onPick: () => onChange(apply(v)),
          }));

      const groups: FilterGroup[] = [];
      if (term !== "") {
        const hits = filterFindings(findings, { ...selection, search: term }).length;
        groups.push({
          options: [
            {
              key: "search",
              content: (
                <span>
                  Search for <span class="font-semibold">“{term}”</span>
                  <span class="text-muted"> in titles, advisories, repositories and owners</span>
                </span>
              ),
              aside: `${formatCount(hits)} found`,
              onPick: () => onChange({ ...selection, search: term }),
            },
          ],
        });
      }
      groups.push({
        label: "Severity",
        options: vocab(SEVERITIES, SEVERITY_TITLES, counts.severity, selection.severities, (v) => ({ ...selection, severities: toggle(selection.severities, v) }), true),
      });
      groups.push({
        label: "Phase",
        options: vocab(PHASES, PHASE_TITLES, counts.phase, selection.phases, (v) => ({ ...selection, phases: toggle(selection.phases, v) }), false),
      });
      groups.push({
        label: "Verdict",
        options: vocab(VERDICTS, VERDICT_TITLES, counts.verdict, selection.verdicts, (v) => ({ ...selection, verdicts: toggle(selection.verdicts, v) }), true),
      });
      const [ranked, total] = rank(repos, repoKeys, needle, 6, (r) => counts.repo.get(r) ?? 0);
      groups.push({
        label: term ? "Repositories" : "Repositories with most findings",
        note: total > ranked.length ? `${formatCount(total)} found` : undefined,
        options: ranked.map(({ item, at }) => ({
          key: `repo:${item}`,
          content: (
            <span class="font-mono">
              <Highlight text={item} at={at} len={term.length} />
            </span>
          ),
          aside: n(counts.repo, item),
          selected: selection.repo === item,
          onPick: () => onChange({ ...selection, repo: selection.repo === item ? "" : item }),
        })),
      });
      return groups;
    },
    [findings, repos, repoKeys, counts, selection, onChange],
  );

  const chips: FilterChip[] = [
    ...(selection.search.trim() ? [{ id: "search", kind: "Text", value: `“${selection.search.trim()}”` }] : []),
    ...[...selection.severities].map((s) => ({ id: `sev:${s}`, kind: "Severity", value: SEVERITY_TITLES[s] })),
    ...[...selection.phases].map((p) => ({ id: `phase:${p}`, kind: "Phase", value: PHASE_TITLES[p] })),
    ...[...selection.verdicts].map((v) => ({ id: `verdict:${v}`, kind: "Verdict", value: VERDICT_TITLES[v] })),
    ...(selection.repo ? [{ id: "repo", kind: "Repository", value: selection.repo, mono: true }] : []),
  ];

  const onRemove = (id: string) => {
    const [kind, v] = id.split(/:(.*)/s);
    if (kind === "search") onChange({ ...selection, search: "" });
    else if (kind === "repo") onChange({ ...selection, repo: "" });
    else if (kind === "sev") onChange({ ...selection, severities: toggle(selection.severities, v as Level) });
    else if (kind === "phase") onChange({ ...selection, phases: toggle(selection.phases, v as Phase) });
    else if (kind === "verdict") onChange({ ...selection, verdicts: toggle(selection.verdicts, v as Recommendation) });
  };

  return (
    <FilterBox
      label="Filter findings"
      placeholder="Filter by text, severity, phase, verdict or repository"
      chips={chips}
      onRemove={onRemove}
      suggest={suggest}
      empty={(term) => `Nothing matches “${term}”.`}
      shortcut
    />
  );
}
