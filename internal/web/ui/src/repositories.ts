// The repositories page's pure logic: the phase groups its columns show,
// the findings-list links each count opens, and the plain-words reading of
// an access verdict.

import type { AccessCheck, ForgeAccess, Phase, RepositoryAccess, RepositorySummary } from "./types";
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
// finding and no rollup. Only these are probed, and only these may be
// backfilled from the page.
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
