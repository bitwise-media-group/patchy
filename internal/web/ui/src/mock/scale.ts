// Demo scale: a deterministic long tail of repository rollups, so the demo
// looks like an organisation with ~11k repositories (one real adopter has
// that many in a single org) and the metrics filter and tables are
// exercised at that size. The hand-written shards stay the head of the
// distribution; the total, model and harness shards are scaled by the same
// factor so every "share of" figure stays coherent.

import type { MonthlyBucket, Rollup, StageAggregate } from "../types";

export const TAIL_REPOSITORIES = 11_000;

// mulberry32: tiny seeded PRNG — the same tail on every load.
function prng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const DOMAINS = [
  "payments", "ledger", "identity", "search", "catalog", "checkout", "media", "billing", "notify", "edge",
  "platform", "data", "ml", "mobile", "web", "admin", "reporting", "inventory", "shipping", "auth",
  "pricing", "fraud", "loyalty", "content", "orders", "support", "analytics", "growth", "risk", "storage",
];
const KINDS = [
  "api", "service", "worker", "ui", "sdk", "cli", "gateway", "jobs", "proxy", "schema",
  "infra", "lambda", "etl", "sync", "events", "terraform", "helm", "docs", "e2e", "client",
];
const VARIANTS = [
  "", "-v2", "-v3", "-legacy", "-eu", "-us", "-apac", "-internal", "-public", "-next",
  "-core", "-lite", "-batch", "-stream", "-admin", "-ops", "-sandbox", "-poc", "-tools", "-config",
];

function tailNames(rand: () => number, n: number, taken: Set<string>): string[] {
  const all: string[] = [];
  for (const d of DOMAINS) for (const k of KINDS) for (const v of VARIANTS) all.push(`${d}-${k}${v}`);
  // Fisher–Yates with the seeded PRNG.
  for (let i = all.length - 1; i > 0; i--) {
    const j = Math.floor(rand() * (i + 1));
    [all[i], all[j]] = [all[j], all[i]];
  }
  return all
    .map((name) => `bitwise-media-group/${name}`)
    .filter((k) => !taken.has(k))
    .slice(0, n);
}

const round = (n: number) => Math.round(n);

function tailRollup(key: string, rand: () => number): Rollup {
  // Heavy tail: most repositories close one or two findings, a few dozens.
  const findings = Math.max(1, Math.floor(Math.pow(rand(), 3.2) * 34) + (rand() < 0.55 ? 0 : 1));
  const remediated = round(findings * (0.35 + rand() * 0.35));
  const failed = Math.min(findings - remediated, round(findings * rand() * 0.12));
  const handedoff = Math.min(findings - remediated - failed, round(findings * rand() * 0.14));
  const dismissed = findings - remediated - failed - handedoff;
  const phases: Record<string, number> = { remediated, dismissed, handedoff, failed };
  for (const k of Object.keys(phases)) if (phases[k] === 0) delete phases[k];
  const remediate = remediated + failed;
  const manual = handedoff;
  const recommendations: Record<string, number> = { remediate, ignore: findings - remediate - manual, manual };
  for (const k of Object.keys(recommendations)) if (recommendations[k] <= 0) delete recommendations[k];
  const invRuns = findings + (rand() < 0.15 ? 1 : 0);
  const remRuns = remediate + (rand() < 0.2 ? 1 : 0);
  const first = new Date(Date.UTC(2025, 4, 3) + Math.floor(rand() * 420) * 86_400_000).toISOString();
  return {
    scope: { type: "repository", key },
    firstProcessed: first,
    lastProcessed: "2026-07-21T08:45:00Z",
    bucket: {
      findings,
      phases,
      recommendations,
      attempts: findings + round(rand() * findings * 0.3),
      stages: {
        investigation: {
          runs: invRuns,
          succeeded: invRuns - (rand() < 0.08 ? 1 : 0),
          costMicroUSD: round(invRuns * (520_000 + rand() * 380_000)),
          elapsedMilliseconds: round(invRuns * (300_000 + rand() * 240_000)),
        },
        remediation: remRuns
          ? {
              runs: remRuns,
              succeeded: remRuns - (rand() < 0.15 ? 1 : 0),
              costMicroUSD: round(remRuns * (1_050_000 + rand() * 700_000)),
              elapsedMilliseconds: round(remRuns * (720_000 + rand() * 600_000)),
            }
          : { runs: 0 },
      },
    },
  };
}

function scaleStage(s: StageAggregate, f: number): StageAggregate {
  const out: StageAggregate = { ...s };
  for (const k of [
    "runs",
    "succeeded",
    "inputTokens",
    "outputTokens",
    "cacheReadTokens",
    "cacheCreationTokens",
    "costMicroUSD",
    "elapsedMilliseconds",
    "turns",
  ] as const) {
    if (s[k] !== undefined) out[k] = round(s[k]! * f);
  }
  if (s.outcomes) out.outcomes = Object.fromEntries(Object.entries(s.outcomes).map(([k, v]) => [k, round(v * f)]));
  if (s.estimate) {
    out.estimate = Object.fromEntries(Object.entries(s.estimate).map(([k, v]) => [k, round(v * f)]));
  }
  return out;
}

function scaleRecord(r: Record<string, number> | undefined, f: number) {
  return r && Object.fromEntries(Object.entries(r).map(([k, v]) => [k, round(v * f)]));
}

// withScale returns the hand-written shards plus the tail, with the total
// recomputed over every repository and the agent shards scaled to match.
export function withScale(rollups: Rollup[]): Rollup[] {
  const rand = prng(0x5eed);
  const head = rollups.filter((r) => r.scope.type === "repository");
  const taken = new Set(head.map((r) => r.scope.key ?? ""));
  const tail = tailNames(rand, TAIL_REPOSITORIES, taken).map((k) => tailRollup(k, rand));
  const repos = [...head, ...tail];

  const sum = (pick: (r: Rollup) => number | undefined) => repos.reduce((a, r) => a + (pick(r) ?? 0), 0);
  const total = rollups.find((r) => r.scope.type === "total")!;
  const f = sum((r) => r.bucket.findings) / (total.bucket.findings ?? 1);

  const merged = (pick: (r: Rollup) => Record<string, number> | undefined) => {
    const out: Record<string, number> = {};
    for (const r of repos) for (const [k, v] of Object.entries(pick(r) ?? {})) out[k] = (out[k] ?? 0) + v;
    return out;
  };

  const scaledTotal: Rollup = {
    ...total,
    bucket: {
      findings: sum((r) => r.bucket.findings),
      phases: merged((r) => r.bucket.phases),
      recommendations: merged((r) => r.bucket.recommendations),
      attempts: sum((r) => r.bucket.attempts),
      // Stage detail (tokens, outcomes, estimates) only exists on the
      // hand-written total, so scale it rather than sum sparse tail stages.
      stages: Object.fromEntries(Object.entries(total.bucket.stages ?? {}).map(([k, s]) => [k, scaleStage(s, f)])),
    },
    monthly: Object.fromEntries(
      Object.entries(total.monthly ?? {}).map(([m, b]): [string, MonthlyBucket] => [
        m,
        { findings: round((b.findings ?? 0) * f), runs: round((b.runs ?? 0) * f), costMicroUSD: round((b.costMicroUSD ?? 0) * f) },
      ]),
    ),
  };

  const agents = rollups
    .filter((r) => r.scope.type === "model" || r.scope.type === "harness")
    .map((r) => ({
      ...r,
      bucket: {
        ...r.bucket,
        phases: scaleRecord(r.bucket.phases, f),
        stages: Object.fromEntries(Object.entries(r.bucket.stages ?? {}).map(([k, s]) => [k, scaleStage(s, f)])),
      },
    }));

  return [scaledTotal, ...repos, ...agents];
}
