// The findings selection, and an offline twin of the server's findings query.
//
// Findings grow faster than anything else (an onboarding backlog runs to
// millions), so the live page never holds them all: GET /api/findings
// filters, sorts and pages server-side, and GET /api/findings/suggest answers
// the filter's type-ahead (internal/web/findingquery.go). queryFindingsLocal
// and suggestFindingsLocal below answer the same requests from a full local
// list (the demo kit and the snapshot), with the same semantics, so the UI
// has one code path whatever the data mode. Keep the two in step.

import type {
  FindingFacets,
  FindingSuggestions,
  FindingSummary,
  FindingsQuery,
  Level,
  Phase,
  Recommendation,
  RepositoryCount,
} from "./types";
import { PHASE_ORDER, TERMINAL_PHASES } from "./types";
import { locate } from "./typeahead";

export interface Selection {
  phases: Set<Phase>;
  severities: Set<Level>;
  verdicts: Set<Recommendation>;
  // The repository terms, any of which may match: exact names, and
  // lower-cased name prefixes and substrings.
  repos: Set<string>;
  repoPrefixes: Set<string>;
  repoContains: Set<string>;
  search: string;
}

export function emptySelection(): Selection {
  return {
    phases: new Set(),
    severities: new Set(),
    verdicts: new Set(),
    repos: new Set(),
    repoPrefixes: new Set(),
    repoContains: new Set(),
    search: "",
  };
}

export function isFiltered(sel: Selection): boolean {
  const sets = [sel.phases, sel.severities, sel.verdicts, sel.repos, sel.repoPrefixes, sel.repoContains];
  return sets.some((s) => s.size > 0) || sel.search.trim() !== "";
}

// selectionQuery is the selection as query parameters. Values are sorted so
// the same selection always makes the same query (and the same cache key).
export function selectionQuery(sel: Selection): FindingsQuery {
  const q: FindingsQuery = {};
  if (sel.phases.size) q.phase = [...sel.phases].sort();
  if (sel.severities.size) q.severity = [...sel.severities].sort();
  if (sel.verdicts.size) q.verdict = [...sel.verdicts].sort();
  if (sel.repos.size) q.repo = [...sel.repos].sort();
  if (sel.repoPrefixes.size) q.repoPrefix = [...sel.repoPrefixes].sort();
  if (sel.repoContains.size) q.repoContains = [...sel.repoContains].sort();
  const text = sel.search.trim();
  if (text) q.q = text;
  return q;
}

const PHASES: Phase[] = [...PHASE_ORDER, ...TERMINAL_PHASES];
const SEVERITIES: Level[] = ["critical", "high", "medium", "low"];
const VERDICTS: Recommendation[] = ["remediate", "ignore", "manual"];

// selectionSearch is the selection as a URL query string (the list route's
// ?query): the API's own parameters, so a link reads like the request.
export function selectionSearch(sel: Selection): string {
  return queryParams(selectionQuery(sel));
}

// selectionFromSearch reads a list route's query string back into a
// selection. Values it does not know are dropped, not errors: a link from a
// newer or older page still opens.
export function selectionFromSearch(search: string): Selection {
  const p = new URLSearchParams(search);
  const known = <T extends string>(name: string, allowed: readonly T[]) =>
    new Set(p.getAll(name).filter((v): v is T => (allowed as readonly string[]).includes(v)));
  const terms = (name: string, lower: boolean) =>
    new Set(
      p
        .getAll(name)
        .map((v) => (lower ? v.trim().toLowerCase() : v.trim()))
        .filter(Boolean),
    );
  return {
    phases: known("phase", PHASES),
    severities: known("severity", SEVERITIES),
    verdicts: known("verdict", VERDICTS),
    repos: terms("repo", false),
    repoPrefixes: terms("repoPrefix", true),
    repoContains: terms("repoContains", true),
    search: p.get("q")?.trim() ?? "",
  };
}

// queryParams renders a query as a URL query string.
export function queryParams(q: FindingsQuery): string {
  const params = new URLSearchParams();
  for (const v of q.phase ?? []) params.append("phase", v);
  for (const v of q.severity ?? []) params.append("severity", v);
  for (const v of q.verdict ?? []) params.append("verdict", v);
  for (const v of q.repo ?? []) params.append("repo", v);
  for (const v of q.repoPrefix ?? []) params.append("repoPrefix", v);
  for (const v of q.repoContains ?? []) params.append("repoContains", v);
  if (q.q) params.set("q", q.q);
  if (q.limit !== undefined) params.set("limit", String(q.limit));
  if (q.cursor) params.set("cursor", q.cursor);
  return params.toString();
}

// ---- the offline twin ------------------------------------------------------

// The limits mirror the server's defaultFindingLimit, maxFindingLimit,
// facetRepositories and maxSuggestLimit.
const DEFAULT_LIMIT = 50;
const MAX_LIMIT = 200;
const FACET_REPOSITORIES = 6;
const MAX_SUGGEST = 20;

function matchesSearch(f: FindingSummary, needle: string): boolean {
  const fields = [f.name, f.title, f.ruleID, f.repository?.name, ...(f.advisories ?? []), ...(f.owners ?? [])];
  return fields.some((v) => v !== undefined && v.toLowerCase().includes(needle));
}

// repoTermMatch matches a lower-cased term against the name part of an
// "owner/name" repository, or the whole of it when the term holds a "/":
// in a single-organisation install every repository shares the owner, so
// "media" should not match them all. Mirrors the server's.
export function repoTermMatch(repo: string, term: string, prefix: boolean): boolean {
  const key = (term.includes("/") ? repo : repo.slice(repo.lastIndexOf("/") + 1)).toLowerCase();
  return prefix ? key.startsWith(term) : key.includes(term);
}

function matches(f: FindingSummary, q: FindingsQuery, needle: string): boolean {
  if (q.phase?.length && (!f.phase || !q.phase.includes(f.phase))) return false;
  if (q.severity?.length && (!f.severity || !q.severity.includes(f.severity))) return false;
  if (q.verdict?.length) {
    const verdict = f.investigation?.recommendation;
    if (!verdict || !q.verdict.includes(verdict)) return false;
  }
  if (q.repo?.length || q.repoPrefix?.length || q.repoContains?.length) {
    const repo = f.repository?.name;
    if (!repo) return false;
    const hit =
      (q.repo ?? []).includes(repo) ||
      (q.repoPrefix ?? []).some((t) => repoTermMatch(repo, t.trim().toLowerCase(), true)) ||
      (q.repoContains ?? []).some((t) => repoTermMatch(repo, t.trim().toLowerCase(), false));
    if (!hit) return false;
  }
  return needle === "" || matchesSearch(f, needle);
}

const SEVERITY_RANK: Record<Level, number> = { critical: 0, high: 1, medium: 2, low: 3 };

// PHASE_RANK orders active work first (rail order), then the closed phases.
const PHASE_RANK: Record<Phase, number> = Object.fromEntries([
  ...PHASE_ORDER.map((p, i) => [p, i]),
  ["Failed", 20],
  ["Dismissed", 21],
  ["HandedOff", 22],
]) as Record<Phase, number>;

// Go's zero time in Unix seconds: what the server sorts an unstamped finding by.
const ZERO_UNIX = -62135596800;

interface SortKey {
  severity: number;
  phase: number;
  observed: number; // Unix seconds
  name: string;
}

function keyOf(f: FindingSummary): SortKey {
  const stamp = f.firstObservedAt || f.createdAt;
  return {
    severity: f.severity ? SEVERITY_RANK[f.severity] : 9,
    phase: f.phase ? PHASE_RANK[f.phase] : 30,
    observed: stamp ? Math.floor(Date.parse(stamp) / 1000) : ZERO_UNIX,
    name: f.name,
  };
}

const byString = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);

// compareKeys is the list order: most severe first, then phase, then most
// recently observed, then name, so the order is total.
function compareKeys(a: SortKey, b: SortKey): number {
  return a.severity - b.severity || a.phase - b.phase || b.observed - a.observed || byString(a.name, b.name);
}

// The cursor matches the server's encoding: base64url over
// "severity.phase.observed.name", the name last because it may hold dots.
function encodeCursor(k: SortKey): string {
  return btoa(`${k.severity}.${k.phase}.${k.observed}.${k.name}`)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

function decodeCursor(s: string): SortKey | null {
  try {
    const raw = atob(s.replace(/-/g, "+").replace(/_/g, "/"));
    const m = /^(-?\d+)\.(-?\d+)\.(-?\d+)\.(.+)$/s.exec(raw);
    if (!m) return null;
    return { severity: Number(m[1]), phase: Number(m[2]), observed: Number(m[3]), name: m[4] };
  } catch {
    return null;
  }
}

// rankRepositories mirrors the server's: an empty needle ranks by findings;
// a term ranks by typeahead.locate, heaviest first among equals, then name.
function rankRepositories(counts: Map<string, number>, needle: string, limit: number): [RepositoryCount[], number] {
  const scored: { r: RepositoryCount; score: number }[] = [];
  for (const [name, findings] of counts) {
    let score = 0;
    if (needle) {
      const lower = name.toLowerCase();
      if (!lower.includes(needle)) continue;
      score = locate(lower, needle)[1];
    }
    scored.push({ r: { name, findings }, score });
  }
  scored.sort((a, b) => a.score - b.score || b.r.findings - a.r.findings || byString(a.r.name, b.r.name));
  return [scored.slice(0, limit).map((x) => x.r), scored.length];
}

function repositoryCounts(all: FindingSummary[]): Map<string, number> {
  const m = new Map<string, number>();
  for (const f of all) {
    const r = f.repository?.name;
    if (r) m.set(r, (m.get(r) ?? 0) + 1);
  }
  return m;
}

function facetsOf(all: FindingSummary[]): FindingFacets {
  const out: FindingFacets = {
    total: all.length,
    open: 0,
    running: 0,
    suspended: 0,
    phases: {},
    severities: {},
    verdicts: {},
    repositoryCount: 0,
  };
  const bump = <K extends string>(m: Partial<Record<K, number>>, k: K) => (m[k] = (m[k] ?? 0) + 1);
  for (const f of all) {
    if (f.phase) {
      bump(out.phases!, f.phase);
      if (!TERMINAL_PHASES.has(f.phase)) out.open += 1;
    }
    if (f.activeRun) out.running += 1;
    if (f.suspend) out.suspended += 1;
    if (f.severity) bump(out.severities!, f.severity);
    const verdict = f.investigation?.recommendation;
    if (verdict) bump(out.verdicts!, verdict);
  }
  const repos = repositoryCounts(all);
  out.repositoryCount = repos.size;
  [out.repositories] = rankRepositories(repos, "", FACET_REPOSITORIES);
  return out;
}

export interface LocalPage<T extends FindingSummary> {
  findings: T[];
  matched: number;
  nextCursor?: string;
  facets?: FindingFacets;
}

// queryFindingsLocal is GET /api/findings from a full list. A cursor it
// cannot read throws, as the server answers 400.
export function queryFindingsLocal<T extends FindingSummary>(all: T[], q: FindingsQuery): LocalPage<T> {
  const limit = Math.min(q.limit ?? DEFAULT_LIMIT, MAX_LIMIT);
  const after = q.cursor ? decodeCursor(q.cursor) : null;
  if (q.cursor && !after) throw new Error("bad query: cursor is not one this server issued");
  const needle = (q.q ?? "").trim().toLowerCase();
  let matched = 0;
  const rows: { f: T; k: SortKey }[] = [];
  for (const f of all) {
    if (!matches(f, q, needle)) continue;
    matched += 1;
    const k = keyOf(f);
    if (after && compareKeys(k, after) <= 0) continue;
    rows.push({ f, k });
  }
  rows.sort((a, b) => compareKeys(a.k, b.k));
  const page: LocalPage<T> = { findings: rows.slice(0, limit).map((x) => x.f), matched };
  if (limit > 0 && rows.length > limit) page.nextCursor = encodeCursor(rows[limit - 1].k);
  if (!after) page.facets = facetsOf(all);
  return page;
}

// suggestFindingsLocal is GET /api/findings/suggest from a full list.
export function suggestFindingsLocal(all: FindingSummary[], q: FindingsQuery): FindingSuggestions {
  const limit = Math.min(q.limit ?? FACET_REPOSITORIES, MAX_SUGGEST);
  const needle = (q.q ?? "").trim().toLowerCase();
  const matched = all.filter((f) => matches(f, q, needle)).length;
  const counts = repositoryCounts(all);
  const [repositories, repositoryTotal] = rankRepositories(counts, needle, limit);
  const prefix = { repositories: 0, findings: 0 };
  const contains = { repositories: 0, findings: 0 };
  if (needle) {
    for (const [name, n] of counts) {
      if (repoTermMatch(name, needle, true)) (prefix.repositories += 1), (prefix.findings += n);
      if (repoTermMatch(name, needle, false)) (contains.repositories += 1), (contains.findings += n);
    }
  }
  return { matched, repositories, repositoryTotal, prefix, contains };
}
