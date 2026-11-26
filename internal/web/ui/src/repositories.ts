// The repositories page's pure logic: the phase groups its columns show,
// the findings-list links each count opens, the plain-words reading of an
// access verdict, and which backfill a row offers.

import type {
  AccessCheck,
  BackfillPhase,
  DatasetUser,
  ForgeAccess,
  GitHubLink,
  Phase,
  RepositoriesPayload,
  RepositoryAccess,
  RepositoryBackfill,
  RepositoryBackfillResult,
  RepositorySummary,
} from "./types";
import { queryParams } from "./filters";
import { hrefForList, hrefForMetrics } from "./router";
import { ownerName } from "./rollups";

// GroupTone follows the status pills' grammar: seedling is in flight, amber
// needs a person, green is done, red failed; soil is new and faint closed.
export type GroupTone = "soil" | "amber" | "seedling" | "green" | "red" | "faint";

export interface PhaseGroup {
  id: string;
  label: string;
  phases: Phase[];
  tone: GroupTone;
}

// PHASE_GROUPS are the stages a row breaks its findings into, in pipeline
// order.
export const PHASE_GROUPS: PhaseGroup[] = [
  { id: "open", label: "Open", phases: ["Opened", "Enhanced", "Investigating"], tone: "soil" },
  { id: "approval", label: "Awaiting approval", phases: ["AwaitingApproval"], tone: "amber" },
  { id: "progress", label: "In progress", phases: ["Queued", "Remediating"], tone: "seedling" },
  { id: "review", label: "In review", phases: ["InReview"], tone: "seedling" },
  { id: "remediated", label: "Remediated", phases: ["Remediated"], tone: "green" },
  { id: "failed", label: "Failed", phases: ["Failed"], tone: "red" },
  { id: "closed", label: "Closed", phases: ["Dismissed", "HandedOff"], tone: "faint" },
];

export function groupCount(summary: RepositorySummary | undefined, group: PhaseGroup): number {
  return group.phases.reduce((n, p) => n + (summary?.phases?.[p] ?? 0), 0);
}

// findingsHref links the findings list filtered to the repository and,
// optionally, phases: the same query string the list itself writes, so the
// link lands on the canonical URL.
export function findingsHref(repo: string, phases: Phase[] = []): string {
  return findingsHrefFor([repo], phases);
}

// findingsHrefFor is findingsHref over several repositories at once: the
// headline's "waiting for approval" across everything tracked.
export function findingsHrefFor(repos: string[], phases: Phase[] = []): string {
  return hrefForList(
    queryParams({ repo: [...repos].sort(), phase: phases.length ? [...phases].sort() : undefined }),
  );
}

// ownerGroups splits names by owner, keeping first-seen order both for the
// owners and within each, so a list reads the way it was built.
export function ownerGroups(names: string[]): { owner: string; repos: string[] }[] {
  const out: { owner: string; repos: string[] }[] = [];
  for (const name of names) {
    const owner = ownerName(name);
    let g = out.find((x) => x.owner === owner);
    if (!g) out.push((g = { owner, repos: [] }));
    g.repos.push(name);
  }
  return out;
}

export function metricsHref(repo: string): string {
  return hrefForMetrics({ kind: "repository", key: repo });
}

// neverSeen reports a repository patchy has no record of: no current
// finding and no rollup. Only these are probed for access.
export function neverSeen(summary: RepositorySummary | undefined): boolean {
  return !!summary && summary.findings === 0 && !summary.rollup;
}

const FAMILY = { codeScanning: "code scanning", secretScanning: "secret scanning" } as const;

// accessProblems explains, one line per integration, why no integration can
// read the repository's alerts.
export function accessProblems(access: RepositoryAccess): string[] {
  const integrations = access.integrations ?? [];
  if (integrations.length === 0) {
    return ["No GitHub integration ingests code or secret scanning alerts, so there is nothing to backfill with."];
  }
  const lines: string[] = [];
  for (const ia of integrations) {
    const checks = (Object.keys(FAMILY) as (keyof typeof FAMILY)[])
      .map((k) => ({ family: FAMILY[k], check: ia.alerts[k] }))
      .filter((c) => c.check);
    const first = checks[0]?.check;
    if (first?.status === "not_installed") {
      lines.push(`The GitHub App of integration ${ia.name} is not installed on ${access.name}.`);
      continue;
    }
    for (const { family, check } of checks) {
      const says = check!.detail ? ` GitHub says: “${check!.detail}”` : "";
      switch (check!.status) {
        case "forbidden":
          lines.push(
            `Integration ${ia.name} may not read ${family} alerts: it needs ` +
              `${family === "code scanning" ? "security_events: read" : "secret_scanning_alerts: read"}, ` +
              `and GitHub Advanced Security must be on.${says}`,
          );
          break;
        case "no_analysis":
          lines.push(`Code scanning has never analysed ${access.name}, so integration ${ia.name} has no alerts to read.`);
          break;
        case "not_found":
          lines.push(`Integration ${ia.name} cannot see ${family} alerts on ${access.name}.${says}`);
          break;
        case "error":
          lines.push(`Integration ${ia.name} could not check ${family} alerts.${says}`);
          break;
        default:
          break;
      }
    }
  }
  return lines;
}

// forgeProblem explains a Forge gap, or returns null when remediation can
// push. It never blocks a backfill: findings still arrive and are
// investigated, but no pull request can be opened.
export function forgeProblem(repo: string, forge?: ForgeAccess): string | null {
  if (!forge) return null;
  switch (forge.status) {
    case "ok":
      return null;
    case "missing":
      return `No Forge covers ${repo}, so patchy cannot open pull requests there.`;
    case "no_write":
      return (
        `Forge ${forge.name ?? ""} can ${forge.contents === "read" ? "only read" : "not access"} the contents of ` +
        `${repo}. Remediation needs contents: write.`
      );
    case "ambiguous":
      return `More than one Forge covers ${repo} equally, so remediation cannot pick one.`;
    default:
      return `The Forge check for ${repo} failed${forge.detail ? `: ${forge.detail}` : "."}`;
  }
}

// accessReason is the two-or-three-word reason a repository's alerts cannot
// be read, for the row's pill; accessProblems carries the full story.
export function accessReason(access: RepositoryAccess): string {
  const checks: AccessCheck[] = (access.integrations ?? []).flatMap((ia) =>
    [ia.alerts.codeScanning, ia.alerts.secretScanning].filter((c): c is AccessCheck => !!c),
  );
  if (checks.length === 0) return "no scanning integration";
  const has = (status: AccessCheck["status"]) => checks.find((c) => c.status === status);
  if (has("not_installed")) return "App not installed";
  const forbidden = has("forbidden");
  if (forbidden) return /advanced security/i.test(forbidden.detail ?? "") ? "Advanced Security off" : "missing permission";
  if (has("no_analysis")) return "never scanned";
  const notFound = has("not_found");
  if (notFound) return /disabled/i.test(notFound.detail ?? "") ? "scanning disabled" : "not visible";
  return "check failed";
}

// forgeBadge is the short label for a Forge gap, or null when remediation
// can push.
export function forgeBadge(forge?: ForgeAccess): string | null {
  switch (forge?.status) {
    case undefined:
    case "ok":
      return null;
    case "missing":
      return "No Forge";
    case "no_write":
      return "Forge is read-only";
    case "ambiguous":
      return "Forge is ambiguous";
    default:
      return "Forge check failed";
  }
}

// ---- backfill --------------------------------------------------------------

// anyConnected reports a GitHub account linked through any Integration.
export function anyConnected(links?: GitHubLink[]): boolean {
  return (links ?? []).some((l) => l.connected);
}

// githubHint reports a user who holds scoped maintainer grants on findings
// (approve or expedite on repositories they maintain) but has connected no
// GitHub account, so those grants cannot apply yet: the user menu then
// points at the repositories page, where the connect button is.
export function githubHint(user?: DatasetUser): boolean {
  const scoped = (user?.maintainedActions ?? []).some((v) => v === "approve-maintained" || v === "expedite-maintained");
  return scoped && !!user?.github?.length && !anyConnected(user.github);
}

// BackfillMode is what a row's backfill does: run at once ("backfill"), file
// a request an operator decides ("request"), or nothing the user may do
// ("none").
export type BackfillMode = "backfill" | "request" | "none";

// backfillMode reads the caller's grants. The backfill grant runs at once
// anywhere; backfill-maintained runs at once with a connected GitHub
// account (the server checks the role per repository). refused marks a row
// whose maintained backfill the server turned down (the role is too low),
// which then offers the request instead.
export function backfillMode(
  p: Pick<RepositoriesPayload, "canBackfill" | "canBackfillMaintained" | "canRequestBackfill" | "github"> | null,
  refused = false,
): BackfillMode {
  if (!p) return "none";
  if (p.canBackfill) return "backfill";
  if (p.canBackfillMaintained && anyConnected(p.github) && !refused) return "backfill";
  return p.canRequestBackfill ? "request" : "none";
}

// backfillAvailable reports whether the cooldown lets the repository be
// backfilled now: backfillAvailableAt absent, unreadable, or past.
export function backfillAvailable(summary: RepositorySummary | undefined, now = Date.now()): boolean {
  const at = summary?.backfillAvailableAt;
  if (!at) return true;
  const t = new Date(at).getTime();
  return Number.isNaN(t) || t <= now;
}

// backfillPhase is a row's request phase. A server that predates phases
// sends only pending and error, which read as Pending, Failed or Complete.
export function backfillPhase(bf: RepositoryBackfill): BackfillPhase {
  if (bf.phase) return bf.phase;
  if (bf.pending) return "Pending";
  return bf.error ? "Failed" : "Complete";
}

// backfillOpen reports a request still to finish: awaiting an operator, or
// not walked yet.
export function backfillOpen(bf: RepositoryBackfill | undefined): boolean {
  if (!bf) return false;
  const phase = backfillPhase(bf);
  return phase === "AwaitingApproval" || phase === "Pending";
}

export interface BackfillState {
  phase: BackfillPhase;
  label: string;
  tone: "amber" | "seedling" | "green" | "red" | "soil";
  // The sentence beside the pill.
  text: string;
}

// backfillState is the pill and sentence a row's latest request reads as.
// ago renders a past time ("2h ago").
export function backfillState(bf: RepositoryBackfill, ago: (iso: string) => string): BackfillState {
  const phase = backfillPhase(bf);
  const when = bf.requestedAt ? ` ${ago(bf.requestedAt)}` : "";
  const by = bf.requestedBy ? ` by ${bf.requestedBy}` : "";
  switch (phase) {
    case "AwaitingApproval":
      return {
        phase,
        label: "Awaiting approval",
        tone: "amber",
        text: `Requested${when}${by}. An operator will review it before it runs.`,
      };
    case "Pending":
      return {
        phase,
        label: "Backfill pending",
        tone: "seedling",
        text: `Requested on ${bf.integration}${when}${by}. Its alerts arrive once the controller picks it up.`,
      };
    case "Failed":
      return { phase, label: "Backfill failed", tone: "red", text: bf.error || "The backfill failed." };
    case "Denied":
      return {
        phase,
        label: "Backfill denied",
        tone: "soil",
        text: bf.decisionReason ? `An operator said: “${bf.decisionReason}”` : "An operator denied the request.",
      };
    default: {
      const ran = bf.lastRunAt ? ` ${ago(bf.lastRunAt)}` : "";
      const counts =
        (bf.ingested ?? 0) > 0
          ? `listed ${bf.listed ?? 0} open alert${bf.listed === 1 ? "" : "s"}, ingested ${bf.ingested}.`
          : "it had no open alerts.";
      return { phase, label: "Backfilled", tone: "green", text: `Backfilled on ${bf.integration}${ran}: ${counts}` };
    }
  }
}

// backfillOutcomeMessage is the toast a successful POST earns.
export function backfillOutcomeMessage(repo: string, res: RepositoryBackfillResult): string {
  switch (res.outcome) {
    case "covered":
      return `A backfill already open on ${res.integration} covers ${repo}.`;
    case "awaiting-approval":
      return `Backfill of ${repo} requested. An operator will review it before it runs.`;
    default:
      return `Backfill of ${repo} started on ${res.integration}. Its open alerts arrive as findings shortly.`;
  }
}

// githubConnectHref starts linking a GitHub account through one
// Integration: a full-page navigation, returning to back (a path and hash)
// afterwards.
export function githubConnectHref(integration: string, back: string): string {
  return `/oauth2/github/${encodeURIComponent(integration)}/authorize?originalPath=${encodeURIComponent(back)}`;
}

// githubProfileHref links a connected login to its profile on the host.
export function githubProfileHref(link: GitHubLink): string | undefined {
  return link.login ? `https://${link.host}/${encodeURIComponent(link.login)}` : undefined;
}

// filterImportable narrows the GitHub picker to names containing term,
// case-insensitively.
export function filterImportable<T extends { name: string }>(repos: T[], term: string): T[] {
  const needle = term.trim().toLowerCase();
  return needle ? repos.filter((r) => r.name.toLowerCase().includes(needle)) : repos;
}
