// Metrics logic over the rollup shards: the derived figures, and an offline
// twin of the server's metrics surface for the demo and snapshot modes.
//
// Rollups are sharded one object per scope value and the dimensions never
// cross (there is no repository × model shard), so a slice is always ONE
// dimension. The exception is "match": every repository whose name contains
// a term, summed — the counters are all additive, so a sum of repository
// buckets is exactly the bucket a shard over them would carry.
//
// Repository shards grow with the estate (10k+ in one organisation), so the
// live page never holds them all: GET /api/rollups is a summary and
// GET /api/rollups/repositories pages, searches and aggregates server-side
// (internal/web/rollupquery.go). summarizeRollups and queryRepositoriesLocal
// below answer the same requests from a full local list, with the same
// semantics, so the UI has one code path whatever the data mode.

import type {
  Dataset,
  RepositoryPage,
  RepositoryQuery,
  RepositorySort,
  Rollup,
  RollupBucket,
  StageAggregate,
} from "./types";
import { locate } from "./typeahead";

export type SliceKind = "repository" | "model" | "harness" | "match";

export interface RollupSlice {
  kind: SliceKind;
  key: string;
}

// SUMMARY_REPOSITORIES mirrors the server's summaryRepositories.
export const SUMMARY_REPOSITORIES = 10;
const MAX_LIMIT = 100;

// ---- stage arithmetic -------------------------------------------------------

export interface StageTotals {
  runs: number;
  succeeded: number;
  inputTokens: number;
  outputTokens: number;
  costMicroUSD: number;
  elapsedMilliseconds: number;
}

export function stageTotals(bucket: RollupBucket): StageTotals {
  const out: StageTotals = {
    runs: 0,
    succeeded: 0,
    inputTokens: 0,
    outputTokens: 0,
    costMicroUSD: 0,
    elapsedMilliseconds: 0,
  };
  for (const s of Object.values(bucket.stages ?? {})) {
    out.runs += s.runs ?? 0;
    out.succeeded += s.succeeded ?? 0;
    out.inputTokens += s.inputTokens ?? 0;
    out.outputTokens += s.outputTokens ?? 0;
    out.costMicroUSD += s.costMicroUSD ?? 0;
    out.elapsedMilliseconds += s.elapsedMilliseconds ?? 0;
  }
  return out;
}

// runOutcomes merges every stage's outcome histogram. Shards that carry no
// histogram fall back to succeeded/other, so the strip still renders.
export function runOutcomes(bucket: RollupBucket): Record<string, number> {
  const out: Record<string, number> = {};
  for (const s of Object.values(bucket.stages ?? {})) {
    if (s.outcomes && Object.keys(s.outcomes).length > 0) {
      for (const [k, v] of Object.entries(s.outcomes)) out[k] = (out[k] ?? 0) + v;
    } else if (s.runs) {
      out.ok = (out.ok ?? 0) + (s.succeeded ?? 0);
      const other = s.runs - (s.succeeded ?? 0);
      if (other > 0) out.error = (out.error ?? 0) + other;
    }
  }
  return out;
}

const ratio = (n?: number, d?: number) => (n !== undefined && d ? n / d : undefined);

export function remediatedShare(b: RollupBucket): number | undefined {
  return ratio(b.phases?.remediated ?? (b.phases ? 0 : undefined), b.findings);
}

export function failedCount(b: RollupBucket): number | undefined {
  return b.phases ? (b.phases.failed ?? 0) : undefined;
}

export function costPerRemediation(b: RollupBucket): number | undefined {
  const cost = stageTotals(b).costMicroUSD;
  return cost ? ratio(cost, b.phases?.remediated) : undefined;
}

export function attemptsPerFinding(b: RollupBucket): number | undefined {
  return ratio(b.attempts, b.findings);
}

// carriesFindings: repository-shaped buckets own findings; harness and model
// shards only carry stage aggregates (a finding has no single agent owner).
export function carriesFindings(slice: RollupSlice | null): boolean {
  return slice === null || slice.kind === "repository" || slice.kind === "match";
}

// shortName is a repository key without its owner ("org/repo" → "repo").
export function shortName(key: string): string {
  const slash = key.lastIndexOf("/");
  return slash >= 0 ? key.slice(slash + 1) : key;
}

export const REPO_SORT_LABELS: Record<Exclude<RepositorySort, "relevance">, string> = {
  name: "Repository",
  findings: "Findings",
  remediated: "Remediated",
  failed: "Failed",
  spend: "Spend",
  costPerRemediation: "Cost per remediation",
};

// ---- offline twin of the server -----------------------------------------------

function addRecord(into: Record<string, number>, from?: Record<string, number>) {
  for (const [k, v] of Object.entries(from ?? {})) into[k] = (into[k] ?? 0) + v;
}

function addStage(into: StageAggregate, s: StageAggregate) {
  const keys = [
    "runs",
    "succeeded",
    "inputTokens",
    "outputTokens",
    "cacheReadTokens",
    "cacheCreationTokens",
    "costMicroUSD",
    "elapsedMilliseconds",
    "turns",
  ] as const;
  for (const k of keys) if (s[k] !== undefined) into[k] = (into[k] ?? 0) + (s[k] ?? 0);
  if (s.outcomes) addRecord((into.outcomes ??= {}), s.outcomes);
  if (s.estimate) {
    const e = (into.estimate ??= {});
    for (const [k, v] of Object.entries(s.estimate) as [keyof typeof e, number][]) e[k] = (e[k] ?? 0) + v;
  }
}

// sumRollups adds repository shards into one bucket (server: sumRollups).
function sumRollups(rollups: Rollup[]): Rollup {
  const bucket: RollupBucket = { findings: 0, phases: {}, recommendations: {}, attempts: 0, stages: {} };
  let first: string | undefined;
  let last: string | undefined;
  for (const r of rollups) {
    const b = r.bucket;
    bucket.findings! += b.findings ?? 0;
    bucket.attempts! += b.attempts ?? 0;
    addRecord(bucket.phases!, b.phases);
    addRecord(bucket.recommendations!, b.recommendations);
    for (const [name, s] of Object.entries(b.stages ?? {})) addStage((bucket.stages![name] ??= {}), s);
    if (r.firstProcessed && (!first || r.firstProcessed < first)) first = r.firstProcessed;
    if (r.lastProcessed && (!last || r.lastProcessed > last)) last = r.lastProcessed;
  }
  return { scope: { type: "repository" }, firstProcessed: first, lastProcessed: last, bucket };
}

interface Row {
  r: Rollup;
  key: string;
  findings: number;
  failed: number;
  spend: number;
  remediatedShare: number;
  costPerRemediation: number;
  score: number;
}

function row(r: Rollup): Row {
  const b = r.bucket;
  return {
    r,
    key: r.scope.key ?? "",
    findings: b.findings ?? 0,
    failed: failedCount(b) ?? -1,
    spend: stageTotals(b).costMicroUSD,
    remediatedShare: remediatedShare(b) ?? -1,
    costPerRemediation: costPerRemediation(b) ?? -1,
    score: 0,
  };
}

const byName = (a: Row, b: Row) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0);

// compareRows mirrors the server's compareRows, tie-breaks included.
function compareRows(a: Row, b: Row, sort: RepositorySort, descending: boolean): number {
  let c = 0;
  let dir = descending ? -1 : 1;
  switch (sort) {
    case "relevance":
      c = a.score - b.score || b.findings - a.findings;
      dir = 1;
      break;
    case "name":
      c = byName(a, b);
      break;
    case "findings":
      c = a.findings - b.findings;
      break;
    case "remediated":
      c = a.remediatedShare - b.remediatedShare;
      break;
    case "failed":
      c = a.failed - b.failed;
      break;
    case "spend":
      c = a.spend - b.spend;
      break;
    case "costPerRemediation":
      c = a.costPerRemediation - b.costPerRemediation;
      break;
  }
  if (c !== 0) return dir * Math.sign(c);
  return b.findings - a.findings || byName(a, b);
}

// summarizeRollups is GET /api/rollups from a full list.
export function summarizeRollups(all: Rollup[]): Pick<Dataset, "rollups" | "repositoryCount"> {
  const fixed = all
    .filter((r) => r.scope.type !== "repository")
    .sort((a, b) => (a.scope.type + "\0" + (a.scope.key ?? "")).localeCompare(b.scope.type + "\0" + (b.scope.key ?? "")));
  const repos = all.filter((r) => r.scope.type === "repository").map(row);
  repos.sort((a, b) => compareRows(a, b, "findings", true));
  return {
    rollups: [...fixed, ...repos.slice(0, SUMMARY_REPOSITORIES).map((x) => x.r)],
    repositoryCount: repos.length,
  };
}

// queryRepositoriesLocal is GET /api/rollups/repositories from a full list.
export function queryRepositoriesLocal(all: Rollup[], q: RepositoryQuery): RepositoryPage {
  const term = (q.q ?? "").trim().toLowerCase();
  const sort: RepositorySort = q.sort ?? (term ? "relevance" : "findings");
  const descending = q.order ? q.order === "desc" : sort !== "name" && sort !== "relevance";
  const rows = all.filter((r) => r.scope.type === "repository").map(row);
  const page: RepositoryPage = { total: 0, of: rows.length, offset: 0, repositories: [] };

  let matches = rows;
  if (q.key) {
    const hit = rows.find((x) => x.key === q.key);
    matches = hit ? [hit] : [];
    if (hit) page.rank = rows.filter((x) => x.findings > hit.findings).length + 1;
  } else if (term) {
    matches = [];
    for (const x of rows) {
      const lower = x.key.toLowerCase();
      if (!lower.includes(term)) continue;
      matches.push({ ...x, score: locate(lower, term)[1] });
    }
  }
  page.total = matches.length;
  if (q.aggregate && matches.length > 0) page.aggregate = sumRollups(matches.map((x) => x.r));

  matches.sort((a, b) => compareRows(a, b, sort, descending));
  page.offset = Math.min(q.offset ?? 0, matches.length);
  const limit = Math.min(q.limit ?? 10, MAX_LIMIT);
  page.repositories = matches.slice(page.offset, page.offset + limit).map((x) => x.r);
  return page;
}
