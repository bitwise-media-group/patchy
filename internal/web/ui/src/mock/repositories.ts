// Demo answers for the repositories page: what GET /api/repositories, GET
// /api/repositories/access and POST /api/repositories/backfill would say
// for the demo estate. Counts come from the demo findings and rollups; the
// access verdicts for never-seen repositories come from the table below, so
// the page shows every state: readable, App not installed, Advanced
// Security off, and a Forge gap.

import type {
  Finding,
  Phase,
  RepositoriesPayload,
  RepositoryAccess,
  RepositoryAccessPayload,
  RepositoryBackfill,
  RepositoryBackfillResult,
  Rollup,
} from "../types";

// DEMO_TRACKED seeds the demo's list when this browser has none stored, so
// the demo and the screenshots open on a full page.
export const DEMO_TRACKED = [
  "bitwise-media-group/platform-schema-next",
  "bitwise-media-group/evolve",
  "bitwise-media-group/patchy",
  "bitwise-media-group/billing-proxy",
  "bitwise-media-group/brand-kit",
  "bitwise-media-group/research-notebooks",
  "bitwise-media-group/infra-sandbox",
  "bitwise-media-group/legacy-cms",
];

const ok = { status: "ok" as const };

// UNSEEN is the access verdict of each never-seen demo repository.
const UNSEEN: Record<string, Omit<RepositoryAccess, "name">> = {
  "bitwise-media-group/brand-kit": {
    integrations: [{ name: "gh", alerts: { codeScanning: ok, secretScanning: ok } }],
    forge: { name: "github", status: "ok", contents: "write" },
    backfillIntegration: "gh",
  },
  "bitwise-media-group/research-notebooks": {
    integrations: [{ name: "gh", alerts: { codeScanning: ok, secretScanning: ok } }],
    forge: { name: "github", status: "no_write", contents: "read" },
    backfillIntegration: "gh",
  },
  "bitwise-media-group/infra-sandbox": {
    integrations: [
      {
        name: "gh",
        alerts: {
          codeScanning: { status: "not_installed", detail: "Not Found" },
          secretScanning: { status: "not_installed", detail: "Not Found" },
        },
      },
    ],
    forge: { status: "missing", detail: "no Forge covers bitwise-media-group/infra-sandbox" },
  },
  "bitwise-media-group/legacy-cms": {
    integrations: [
      {
        name: "gh",
        alerts: {
          codeScanning: {
            status: "forbidden",
            detail: "Advanced Security must be enabled for this repository to use code scanning.",
          },
          secretScanning: { status: "not_found", detail: "Secret scanning is disabled on this repository." },
        },
      },
    ],
    forge: { name: "github", status: "ok", contents: "write" },
  },
};

// Requests made from the demo page, by repository.
const demoBackfills = new Map<string, RepositoryBackfill>();

function counts(findings: Finding[], name: string): { findings: number; phases: Partial<Record<Phase, number>> } {
  const phases: Partial<Record<Phase, number>> = {};
  let n = 0;
  for (const f of findings) {
    if (f.repository?.name !== name) continue;
    n++;
    if (f.phase) phases[f.phase] = (phases[f.phase] ?? 0) + 1;
  }
  return { findings: n, phases };
}

function repositoryRollup(rollups: Rollup[], name: string): Rollup | undefined {
  return rollups.find((r) => r.scope.type === "repository" && r.scope.key === name);
}

export function mockRepositories(
  findings: Finding[],
  rollups: Rollup[],
  names: string[],
  canBackfill: boolean,
): RepositoriesPayload {
  return {
    generatedAt: new Date().toISOString(),
    canBackfill,
    accessCheck: true,
    repositories: names.map((name) => {
      const c = counts(findings, name);
      const r = repositoryRollup(rollups, name);
      return {
        name,
        findings: c.findings,
        ...(c.findings ? { phases: c.phases } : {}),
        ...(r ? { rollup: { findings: r.bucket.findings ?? 0, lastProcessed: r.lastProcessed } } : {}),
        ...(demoBackfills.has(name) ? { backfill: { ...demoBackfills.get(name)! } } : {}),
      };
    }),
  };
}

export function mockRepositoryAccess(findings: Finding[], rollups: Rollup[], names: string[]): RepositoryAccessPayload {
  return {
    available: true,
    repositories: names.map((name) => {
      if (counts(findings, name).findings > 0 || repositoryRollup(rollups, name)) {
        return { name, skipped: "known" as const };
      }
      // A repository the table does not know reads like a fresh one the
      // App covers, so anything typed into the demo can be tried.
      return { name, ...(UNSEEN[name] ?? UNSEEN["bitwise-media-group/brand-kit"]) };
    }),
  };
}

// mockRepositoryBackfill records a request and has the demo "controller"
// consume it shortly after.
export function mockRepositoryBackfill(
  findings: Finding[],
  rollups: Rollup[],
  name: string,
  by: string,
): RepositoryBackfillResult {
  const access = mockRepositoryAccess(findings, rollups, [name]).repositories[0];
  if (access.skipped) throw new Error(`Patchy has already seen ${name}, so it needs no backfill.`);
  if (!access.backfillIntegration) throw new Error(`No integration can read the alerts of ${name}.`);
  const integration = access.backfillIntegration;
  const requestedAt = new Date().toISOString();
  demoBackfills.set(name, { integration, requestedBy: by, requestedAt, pending: true });
  setTimeout(() => {
    demoBackfills.set(name, {
      integration,
      requestedBy: by,
      requestedAt,
      lastRunAt: new Date().toISOString(),
      listed: 0,
      ingested: 0,
    });
  }, 1500);
  return { integration, outcome: "requested" };
}
