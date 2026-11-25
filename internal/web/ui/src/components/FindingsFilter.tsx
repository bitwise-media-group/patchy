// The findings filter: the shared FilterBox (same control as the metrics
// page) over the findings selection. Typing offers a free-text search
// first, then whichever severities, phases, verdicts and repositories match
// what was typed, plus "repositories starting with" and "containing" the
// term; every applied filter is a chip in the field. Repository picks add
// up, as severities do: a finding in any picked repository, or any
// repository a picked prefix or substring matches, passes. The tallies
// are the server's facets (every finding, filters aside); a typed term's
// search count and repository matches are asked of the server as you type
// (debounced, stale requests cancelled), since repositories can number tens
// of thousands.

import { useCallback, useEffect, useState } from "preact/hooks";
import type {
  FindingFacets,
  FindingSuggestions,
  Level,
  Phase,
  Recommendation,
  RepositoryCount,
  RepositoryMatch,
} from "../types";
import { PHASE_ORDER } from "../types";
import { PHASE_TITLES, SEVERITY_TITLES, VERDICT_TITLES, formatCount } from "../format";
import { suggestFindings } from "../api";
import { selectionQuery, type Selection } from "../filters";
import { locate } from "../typeahead";
import { FilterBox, Highlight, type FilterChip, type FilterGroup, type FilterOption } from "./FilterBox";

const SEVERITIES: Level[] = ["critical", "high", "medium", "low"];
const VERDICTS: Recommendation[] = ["remediate", "ignore", "manual"];
const PHASES: Phase[] = [...PHASE_ORDER, "Failed", "Dismissed", "HandedOff"];
const REPO_SUGGESTIONS = 6;
const DEBOUNCE_MS = 120;

function toggle<T>(set: Set<T>, v: T): Set<T> {
  const next = new Set(set);
  next.has(v) ? next.delete(v) : next.add(v);
  return next;
}

function reposLabel(m: RepositoryMatch): string {
  return `${formatCount(m.repositories)} ${m.repositories === 1 ? "repo" : "repos"}`;
}

// matchLabel finds term in a vocabulary label, start-of-word first.
function matchLabel(label: string, term: string): number {
  return term === "" ? -1 : label.toLowerCase().indexOf(term.toLowerCase());
}

export function FindingsFilter({
  facets,
  selection,
  onChange,
}: {
  facets: FindingFacets | null;
  selection: Selection;
  onChange: (next: Selection) => void;
}) {
  const [term, setTerm] = useState("");
  // The server's answer for one term, kept while the next is in flight so
  // the list doesn't flash empty between keystrokes.
  const [remote, setRemote] = useState<{ term: string; key: string; answer: FindingSuggestions } | null>(null);
  // The search count depends on the other filters too.
  const selectionKey = JSON.stringify({ ...selectionQuery(selection), q: undefined });

  useEffect(() => {
    if (term === "") return;
    const ctl = new AbortController();
    const timer = setTimeout(() => {
      suggestFindings({ ...selectionQuery(selection), q: term, limit: REPO_SUGGESTIONS }, ctl.signal)
        .then((answer) => setRemote({ term, key: selectionKey, answer }))
        .catch(() => {
          // Aborted (superseded) or failed: the next keystroke retries.
        });
    }, DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      ctl.abort();
    };
    // selection is read through selectionKey, which changes with it.
  }, [term, selectionKey]);

  const suggest = useCallback(
    (t: string): FilterGroup[] => {
      const needle = t.toLowerCase();
      const n = (m: Partial<Record<string, number>> | undefined, k: string) => formatCount(m?.[k] ?? 0);
      const vocab = <K extends string>(
        values: K[],
        titles: Record<K, string>,
        tallyOf: Partial<Record<K, number>> | undefined,
        selected: Set<K>,
        apply: (v: K) => Selection,
        always: boolean,
      ): FilterOption[] =>
        values
          .map((v) => ({ v, at: matchLabel(titles[v], t) }))
          .filter(({ at }) => (t === "" ? always : at >= 0))
          .map(({ v, at }) => ({
            key: v,
            content: <Highlight text={titles[v]} at={at} len={t.length} />,
            aside: n(tallyOf, v),
            selected: selected.has(v),
            onPick: () => onChange(apply(v)),
          }));

      const answer = t !== "" && remote?.term === t ? remote : null;
      const fresh = answer !== null && answer.key === selectionKey;
      const groups: FilterGroup[] = [];
      if (t !== "") {
        groups.push({
          options: [
            {
              key: "search",
              content: (
                <span>
                  Search for <span class="font-semibold">“{t}”</span>
                  <span class="text-muted"> in titles, advisories, repositories and owners</span>
                </span>
              ),
              aside: fresh ? `${formatCount(answer.answer.matched)} found` : "…",
              onPick: () => onChange({ ...selection, search: t }),
            },
          ],
        });
      }
      groups.push({
        label: "Severity",
        options: vocab(SEVERITIES, SEVERITY_TITLES, facets?.severities, selection.severities, (v) => ({ ...selection, severities: toggle(selection.severities, v) }), true),
      });
      groups.push({
        label: "Phase",
        options: vocab(PHASES, PHASE_TITLES, facets?.phases, selection.phases, (v) => ({ ...selection, phases: toggle(selection.phases, v) }), false),
      });
      groups.push({
        label: "Verdict",
        options: vocab(VERDICTS, VERDICT_TITLES, facets?.verdicts, selection.verdicts, (v) => ({ ...selection, verdicts: toggle(selection.verdicts, v) }), true),
      });

      // Before anything is typed, the heaviest repositories come with the
      // facets; a typed term's matches come from the server.
      let repos: RepositoryCount[] = [];
      let total = 0;
      if (t === "") {
        repos = facets?.repositories ?? [];
        total = facets?.repositoryCount ?? 0;
      } else if (remote && remote.term !== "") {
        // The last answer stands in until this term's arrives.
        repos = remote.answer.repositories;
        total = remote.answer.repositoryTotal;
      }
      const repoFresh = t === "" || remote?.term === t;
      // The term as a repository-name filter. Hidden once the server says
      // it matches nothing; "…" until it has answered.
      const term = t.trim().toLowerCase();
      const termOptions: FilterOption[] = [];
      const termOption = (kind: "prefix" | "contains", verb: string, set: Set<string>, field: "repoPrefixes" | "repoContains") => {
        const reach = repoFresh && remote ? remote.answer[kind] : null;
        if (term === "" || (reach && reach.repositories === 0)) return;
        termOptions.push({
          key: `repo-${kind}`,
          content: (
            <span>
              Repositories {verb} <span class="font-semibold">“{term}”</span>
            </span>
          ),
          aside: reach ? reposLabel(reach) : "…",
          selected: set.has(term),
          onPick: () => onChange({ ...selection, [field]: toggle(set, term) }),
        });
      };
      termOption("prefix", "starting with", selection.repoPrefixes, "repoPrefixes");
      termOption("contains", "containing", selection.repoContains, "repoContains");
      groups.push({
        label: t ? "Repositories" : "Repositories with most findings",
        note: !repoFresh ? "Searching…" : total > repos.length ? `${formatCount(total)} found` : undefined,
        options: [
          ...termOptions,
          ...repos.map((r) => ({
            key: `repo:${r.name}`,
            content: (
              <span class="font-mono">
                <Highlight text={r.name} at={t ? locate(r.name.toLowerCase(), needle)[0] : -1} len={t.length} />
              </span>
            ),
            aside: formatCount(r.findings),
            selected: selection.repos.has(r.name),
            onPick: () => onChange({ ...selection, repos: toggle(selection.repos, r.name) }),
          })),
        ],
      });
      return groups;
    },
    [facets, remote, selection, selectionKey, onChange],
  );

  const chips: FilterChip[] = [
    ...(selection.search.trim() ? [{ id: "search", kind: "Text", value: `“${selection.search.trim()}”` }] : []),
    ...[...selection.severities].map((s) => ({ id: `sev:${s}`, kind: "Severity", value: SEVERITY_TITLES[s] })),
    ...[...selection.phases].map((p) => ({ id: `phase:${p}`, kind: "Phase", value: PHASE_TITLES[p] })),
    ...[...selection.verdicts].map((v) => ({ id: `verdict:${v}`, kind: "Verdict", value: VERDICT_TITLES[v] })),
    ...[...selection.repos].map((r) => ({ id: `repo:${r}`, kind: "Repository", value: r, mono: true })),
    ...[...selection.repoPrefixes].map((r) => ({ id: `repoPrefix:${r}`, kind: "Repository starts with", value: r, mono: true })),
    ...[...selection.repoContains].map((r) => ({ id: `repoContains:${r}`, kind: "Repository contains", value: r, mono: true })),
  ];

  const onRemove = (id: string) => {
    const [kind, v] = id.split(/:(.*)/s);
    if (kind === "search") onChange({ ...selection, search: "" });
    else if (kind === "repo") onChange({ ...selection, repos: toggle(selection.repos, v) });
    else if (kind === "repoPrefix") onChange({ ...selection, repoPrefixes: toggle(selection.repoPrefixes, v) });
    else if (kind === "repoContains") onChange({ ...selection, repoContains: toggle(selection.repoContains, v) });
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
      onQuery={setTerm}
      empty={(t) => (remote?.term === t ? `Nothing matches “${t}”.` : "Searching…")}
      shortcut
    />
  );
}
