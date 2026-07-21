// Client-side filtering and sorting over the flat findings list. The server
// never aggregates or filters; this module is the single source of truth.

import type { Finding, Level, Phase, Recommendation } from "./types";
import { PHASE_ORDER } from "./types";

export interface Selection {
  phases: Set<Phase>;
  severities: Set<Level>;
  verdicts: Set<Recommendation>;
  repo: string; // "" = all
  search: string;
}

export function emptySelection(): Selection {
  return { phases: new Set(), severities: new Set(), verdicts: new Set(), repo: "", search: "" };
}

export function hasActiveFilters(sel: Selection): boolean {
  return (
    sel.phases.size > 0 ||
    sel.severities.size > 0 ||
    sel.verdicts.size > 0 ||
    sel.repo !== "" ||
    sel.search.trim() !== ""
  );
}

export function repoOptions(findings: Finding[]): string[] {
  const repos = new Set<string>();
  for (const f of findings) {
    if (f.repository?.name) repos.add(f.repository.name);
  }
  return [...repos].sort();
}

function matchesSearch(f: Finding, needle: string): boolean {
  const haystack = [
    f.name,
    f.title,
    f.ruleID,
    f.repository?.name,
    ...(f.advisories ?? []),
    ...(f.owners ?? []),
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  return haystack.includes(needle);
}

export function filterFindings(findings: Finding[], sel: Selection): Finding[] {
  const needle = sel.search.trim().toLowerCase();
  return findings.filter((f) => {
    if (sel.phases.size > 0 && (!f.phase || !sel.phases.has(f.phase))) return false;
    if (sel.severities.size > 0 && (!f.severity || !sel.severities.has(f.severity))) return false;
    if (sel.verdicts.size > 0) {
      const verdict = f.investigation?.recommendation;
      if (!verdict || !sel.verdicts.has(verdict)) return false;
    }
    if (sel.repo !== "" && f.repository?.name !== sel.repo) return false;
    if (needle !== "" && !matchesSearch(f, needle)) return false;
    return true;
  });
}

const SEVERITY_RANK: Record<Level, number> = { critical: 0, high: 1, medium: 2, low: 3 };

// phaseRank orders active work first (rail order), then terminals.
const PHASE_RANK: Record<Phase, number> = Object.fromEntries([
  ...PHASE_ORDER.map((p, i) => [p, i]),
  ["Failed", 20],
  ["Dismissed", 21],
  ["HandedOff", 22],
]) as Record<Phase, number>;

// sortFindings: needs-attention first — severity, then phase progress, then
// most recently observed.
export function sortFindings(findings: Finding[]): Finding[] {
  return [...findings].sort((a, b) => {
    const sa = a.severity ? SEVERITY_RANK[a.severity] : 9;
    const sb = b.severity ? SEVERITY_RANK[b.severity] : 9;
    if (sa !== sb) return sa - sb;
    const pa = a.phase ? PHASE_RANK[a.phase] : 30;
    const pb = b.phase ? PHASE_RANK[b.phase] : 30;
    if (pa !== pb) return pa - pb;
    return (b.firstObservedAt ?? "").localeCompare(a.firstObservedAt ?? "");
  });
}

// phaseCounts returns per-phase totals plus how many are suspended.
export function phaseCounts(findings: Finding[]): { counts: Map<Phase, number>; suspended: number } {
  const counts = new Map<Phase, number>();
  let suspended = 0;
  for (const f of findings) {
    if (f.phase) counts.set(f.phase, (counts.get(f.phase) ?? 0) + 1);
    if (f.suspend) suspended += 1;
  }
  return { counts, suspended };
}
