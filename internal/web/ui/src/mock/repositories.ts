// Demo answers for the repositories page: what GET /api/repositories, GET
// /api/repositories/access, POST /api/repositories/backfill, the backfill
// requests routes and the GitHub link routes would say for the demo
// estate. Counts come from the demo findings and rollups; the access
// verdicts for never-seen repositories come from the table below, so the
// page shows every state: readable, App not installed, Advanced Security
// off, and a Forge gap. The seeded backfill requests show one awaiting an
// operator, one denied, and one inside its cooldown; the GitHub account
// starts connected, so the import picker has something to list.

import type {
  ActionVerb,
  BackfillRequest,
  BackfillRequestsPayload,
  BackfillVia,
  Finding,
  GitHubLink,
  GitHubRepositoriesPayload,
  GitHubRepository,
  Phase,
  RepositoriesPayload,
  RepositoryAccess,
  RepositoryAccessPayload,
  RepositoryBackfill,
  RepositoryBackfillResult,
  Rollup,
} from "../types";
import type { Persona } from "./personas";

// MockStatusError is a demo answer the live server would send as an HTTP
// error: api.ts turns it into the same error a response would.
export class MockStatusError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

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

const COOLDOWN_SECONDS = 24 * 60 * 60;
const MAX_OPEN_PER_USER = 10;
const GITHUB_LOGIN = "mona";

// The repositories the demo GitHub account administers or maintains.
const MAINTAINED: GitHubRepository[] = [
  { name: "bitwise-media-group/patchy", role: "admin" },
  { name: "bitwise-media-group/evolve", role: "maintain" },
  { name: "bitwise-media-group/platform-schema-next", role: "admin" },
  { name: "bitwise-media-group/brand-kit", role: "maintain" },
  { name: "bitwise-media-group/dotty", role: "maintain" },
  { name: "bitwise-media-group/gh-claude", role: "admin" },
  { name: "bitwise-media-group/design-system", role: "maintain" },
  { name: "bitwise-media-group/bitwise-site", role: "maintain" },
  { name: "bitwise-media-group/dke-podcast", role: "maintain" },
  { name: "bitwise-media-group/infrastructure", role: "admin" },
  { name: "bitwise-media-group/homebrew-tap", role: "maintain" },
  { name: "bitwise-media-group/github-workflows", role: "maintain" },
];

let links: GitHubLink[] = [{ integration: "gh", host: "github.com", login: GITHUB_LOGIN, connected: true }];

const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString();
const MIN = 60_000;
const HOUR = 60 * MIN;

// requests is the demo's BackfillRequest store, newest first. Times are
// relative to page load, so the cooldown below is always live.
let requests: BackfillRequest[] = [
  {
    name: "backfill-k2x9p",
    integration: "gh",
    requestedBy: "sam@acme.test",
    requestedAt: iso(25 * MIN),
    repositories: ["bitwise-media-group/research-notebooks"],
    via: "requested",
    phase: "AwaitingApproval",
  },
  {
    name: "backfill-q8w3n",
    integration: "gh",
    requestedBy: "sam@acme.test",
    requestedAt: iso(70 * MIN),
    repositories: ["bitwise-media-group/legacy-cms"],
    via: "requested",
    phase: "AwaitingApproval",
  },
  {
    name: "backfill-7hq3w",
    integration: "gh",
    requestedBy: "op@acme.test",
    requestedAt: iso(3 * HOUR),
    repositories: ["bitwise-media-group/brand-kit"],
    via: "backfill",
    phase: "Complete",
    attempts: 1,
    lastRunAt: iso(3 * HOUR - MIN),
    completedAt: iso(3 * HOUR - MIN),
  },
  {
    name: "backfill-m4c8d",
    integration: "gh",
    requestedBy: "sam@acme.test",
    requestedAt: iso(50 * HOUR),
    repositories: ["bitwise-media-group/billing-proxy"],
    via: "requested",
    phase: "Denied",
    decision: {
      verdict: "Denied",
      by: "op@acme.test",
      at: iso(49 * HOUR),
      reason: "billing-proxy is being retired this quarter; the payments team tracks its alerts.",
    },
  },
  {
    name: "backfill-a1b2c",
    integration: "gh",
    requestedBy: "mona@acme.test",
    requestedAt: iso(96 * HOUR),
    repositories: ["bitwise-media-group/patchy"],
    via: "maintained",
    githubLogin: GITHUB_LOGIN,
    phase: "Complete",
    attempts: 1,
    lastRunAt: iso(96 * HOUR - MIN),
    completedAt: iso(96 * HOUR - MIN),
    listed: 12,
    ingested: 3,
  },
];

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

// The server's request rules (internal/web/repositories.go and
// backfillrequests.go), ported for the demo.

function covers(filter: string[], repo: string): boolean {
  const full = repo.toLowerCase();
  return filter.some((entry) => {
    let e = entry.toLowerCase();
    if (!e.includes("/")) e += "/";
    return full.startsWith(e);
  });
}

function isOpen(b: BackfillRequest): boolean {
  return b.phase === "AwaitingApproval" || b.phase === "Pending";
}

function repositoryBackfill(repo: string): RepositoryBackfill | undefined {
  let best: BackfillRequest | undefined;
  for (const b of requests) {
    const open = isOpen(b);
    const names = !!b.repositories?.length && covers(b.repositories, repo);
    if (!names && (b.repositories?.length || !open)) continue;
    if (!best || (open && !isOpen(best))) best = b;
    if (open) break;
  }
  if (!best) return undefined;
  const pending = isOpen(best);
  return {
    request: best.name,
    integration: best.integration,
    requestedBy: best.requestedBy,
    requestedAt: best.requestedAt,
    phase: best.phase,
    via: best.via,
    ...(pending ? { pending } : {}),
    ...(best.decision?.reason ? { decisionReason: best.decision.reason } : {}),
    ...(!pending || (best.attempts ?? 0) > 0
      ? { lastRunAt: best.lastRunAt, listed: best.listed, ingested: best.ingested, error: best.error }
      : {}),
  };
}

function cooldownUntil(repo: string, now = Date.now()): number {
  let until = 0;
  for (const b of requests) {
    if (b.phase !== "Complete" || !b.completedAt || !b.repositories?.length || !covers(b.repositories, repo)) continue;
    const t = new Date(b.completedAt).getTime() + COOLDOWN_SECONDS * 1000;
    if (t > now && t > until) until = t;
  }
  return until;
}

export function mockRepositories(
  findings: Finding[],
  rollups: Rollup[],
  names: string[],
  persona: Persona | null,
): RepositoriesPayload {
  return {
    generatedAt: new Date().toISOString(),
    canBackfill: !!persona?.integrationActions.includes("backfill"),
    canBackfillMaintained: !!persona?.maintainedActions.includes("backfill-maintained"),
    canRequestBackfill: !!persona,
    cooldownSeconds: COOLDOWN_SECONDS,
    github: links.map((l) => ({ ...l })),
    accessCheck: true,
    repositories: names.map((name) => {
      const c = counts(findings, name);
      const r = repositoryRollup(rollups, name);
      const bf = repositoryBackfill(name);
      const until = cooldownUntil(name);
      return {
        name,
        findings: c.findings,
        ...(c.findings ? { phases: c.phases } : {}),
        ...(r ? { rollup: { findings: r.bucket.findings ?? 0, lastProcessed: r.lastProcessed } } : {}),
        ...(bf ? { backfill: bf } : {}),
        ...(until ? { backfillAvailableAt: new Date(until).toISOString() } : {}),
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

// finish has the demo "controller" walk a request shortly after.
function finish(name: string): void {
  setTimeout(() => {
    const b = requests.find((x) => x.name === name);
    if (!b || b.phase !== "Pending") return;
    const now = new Date().toISOString();
    Object.assign(b, { phase: "Complete", attempts: 1, lastRunAt: now, completedAt: now, listed: 0, ingested: 0 });
  }, 1500);
}

let seq = 0;
function newName(): string {
  return `backfill-demo${++seq}`;
}

// mockRepositoryBackfill answers POST /api/repositories/backfill the way
// the server would: the persona's grant picks the path, then the
// open-request check and the cooldown apply to every path.
export function mockRepositoryBackfill(
  findings: Finding[],
  rollups: Rollup[],
  repo: string,
  request: boolean,
  persona: Persona,
): RepositoryBackfillResult {
  let via: BackfillVia = "requested";
  if (!request) {
    if (persona.integrationActions.includes("backfill")) via = "backfill";
    else if (persona.maintainedActions.includes("backfill-maintained")) via = "maintained";
    else {
      throw new MockStatusError(
        403,
        `Permission denied. User "${persona.label}" may not backfill integrations in this namespace. ` +
          "Request a backfill instead, and an operator will decide it.",
      );
    }
  }

  const open = requests.find((b) => isOpen(b) && (!b.repositories?.length || covers(b.repositories, repo)));
  if (open) return { integration: open.integration, request: open.name, outcome: "covered" };
  const until = cooldownUntil(repo);
  if (until) {
    throw new MockStatusError(
      429,
      `Patchy backfilled ${repo} recently; try again after ${new Date(until).toISOString().replace(/\.\d+Z$/, "Z")}.`,
    );
  }
  if (
    via === "requested" &&
    requests.filter((b) => b.requestedBy === persona.label && b.phase === "AwaitingApproval").length >=
      MAX_OPEN_PER_USER
  ) {
    throw new MockStatusError(
      429,
      `You have ${MAX_OPEN_PER_USER} backfill requests waiting for an operator already. Try again once they are decided.`,
    );
  }

  let integration = "gh";
  let githubLogin: string | undefined;
  if (via === "maintained") {
    const link = links.find((l) => l.connected);
    if (!link) {
      throw new MockStatusError(403, "Connect GitHub to backfill repositories you maintain, or request a backfill instead.");
    }
    const role = MAINTAINED.find((r) => r.name.toLowerCase() === repo.toLowerCase())?.role ?? "none";
    if (role !== "admin" && role !== "maintain") {
      throw new MockStatusError(
        403,
        `You cannot backfill ${repo} (it needs admin or maintain on GitHub): ` +
          `${link.integration}: @${link.login}'s role is ${role}. Request a backfill instead.`,
      );
    }
    integration = link.integration;
    githubLogin = link.login;
  } else {
    const access = mockRepositoryAccess(findings, rollups, [repo]).repositories[0];
    if (!access.skipped && !access.backfillIntegration) {
      throw new MockStatusError(409, `No integration can read the alerts of ${repo}.`);
    }
    integration = access.backfillIntegration ?? "gh";
  }

  const name = newName();
  requests = [
    {
      name,
      integration,
      requestedBy: persona.label,
      requestedAt: new Date().toISOString(),
      repositories: [repo],
      via,
      ...(githubLogin ? { githubLogin } : {}),
      phase: via === "requested" ? "AwaitingApproval" : "Pending",
    },
    ...requests,
  ];
  if (via !== "requested") finish(name);
  return { integration, request: name, outcome: via === "requested" ? "awaiting-approval" : "requested" };
}

export function mockBackfillRequests(canDecide: boolean): BackfillRequestsPayload {
  return {
    generatedAt: new Date().toISOString(),
    requests: requests.map((b) => ({ ...b, decision: b.decision && { ...b.decision } })),
    canDecide,
  };
}

// mockBackfillDecision approves or denies a requested backfill, once.
export function mockBackfillDecision(name: string, verdict: "approve" | "deny", reason: string, by: string): void {
  const b = requests.find((x) => x.name === name);
  if (!b) throw new MockStatusError(404, "backfill request not found");
  if (b.via !== "requested") throw new MockStatusError(409, "This backfill request needs no decision.");
  if (b.decision) {
    throw new MockStatusError(
      409,
      `${b.decision.by} already ${b.decision.verdict === "Approved" ? "approved" : "denied"} this request.`,
    );
  }
  b.decision = {
    verdict: verdict === "approve" ? "Approved" : "Denied",
    by,
    at: new Date().toISOString(),
    ...(reason ? { reason } : {}),
  };
  b.phase = verdict === "approve" ? "Pending" : "Denied";
  if (verdict === "approve") finish(name);
}

// mockUserActions is a finding's userActions for the persona: its base
// grants, plus approve and expedite on a repository the connected demo
// account maintains when the persona holds the scoped verb, as the
// server's maintained-set cache adds them.
export function mockUserActions(f: Finding, persona: Persona): ActionVerb[] {
  const verbs = new Set<ActionVerb>(persona.grants);
  const repo = f.repository?.name?.toLowerCase();
  const maintained =
    !!repo && links.some((l) => l.connected) && MAINTAINED.some((r) => r.name.toLowerCase() === repo);
  if (maintained) {
    if (persona.maintainedActions.includes("approve-maintained")) verbs.add("approve");
    if (persona.maintainedActions.includes("expedite-maintained")) verbs.add("expedite");
  }
  return [...verbs];
}

export function mockGitHubRepositories(integration: string): GitHubRepositoriesPayload {
  const link = links.find((l) => l.integration === integration);
  if (!link) throw new MockStatusError(404, `Integration ${integration} cannot connect GitHub accounts.`);
  if (!link.connected) throw new MockStatusError(409, `Connect GitHub on ${link.host} first.`);
  return { integration, login: link.login ?? GITHUB_LOGIN, repositories: MAINTAINED.map((r) => ({ ...r })) };
}

// mockGitHubLink connects or disconnects the demo account; a real connect
// is a full-page round trip through GitHub.
export function mockGitHubLink(integration: string, connected: boolean): void {
  links = links.map((l) =>
    l.integration === integration
      ? { ...l, connected, ...(connected ? { login: GITHUB_LOGIN } : { login: undefined }) }
      : l,
  );
}
