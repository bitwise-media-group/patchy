// Demo backlog: a deterministic spread of lean findings behind the
// hand-written ones, so the demo findings list runs to several pages and
// infinite scroll, the facets and the type-ahead are exercised at a
// believable size. Each finding is internally coherent (its phase history,
// run summaries, pull request and tracking state agree with its phase), but
// carries none of the hand-written ones' reports or transcripts. The
// repositories come from the same seeded list as the metrics tail
// (scale.ts), so a repository filtered here also exists on the metrics page.

import type {
  Finding,
  HoldReason,
  InvestigationSummary,
  Level,
  Phase,
  PhaseTime,
  Rating,
  Recommendation,
  RemediationSummary,
} from "../types";
import { prng, tailNames } from "./scale";

export const BACKLOG_FINDINGS = 600;

// The demo's "now" (mockDataset's generatedAt): nothing happens after it.
const NOW = Date.parse("2026-07-21T08:45:00Z");
const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

type Kind = "cs" | "dbot" | "ccs";

interface Rule {
  kind: Kind;
  ruleID: string;
  title: string;
  description: string;
  cwe: string;
  severity: Level;
}

const RULES: Rule[] = [
  { kind: "cs", ruleID: "go/sql-injection", title: "SQL query built from user-controlled value", cwe: "CWE-89", severity: "critical",
    description: "A handler interpolates a request parameter into a SQL string; crafted input can read or modify rows beyond the intended ones." },
  { kind: "cs", ruleID: "js/reflected-xss", title: "Reflected cross-site scripting in search results", cwe: "CWE-79", severity: "high",
    description: "The search term is echoed into the results page without encoding, so a crafted link runs script in the viewer's session." },
  { kind: "cs", ruleID: "go/path-injection", title: "Uncontrolled path used in file read", cwe: "CWE-22", severity: "high",
    description: "A file name from the request is joined onto the data directory without cleaning, letting ../ sequences read outside it." },
  { kind: "cs", ruleID: "go/request-forgery", title: "Server-side request forgery in outbound fetch", cwe: "CWE-918", severity: "high",
    description: "A caller-supplied URL is fetched before its host is checked, so requests can be pointed at internal services." },
  { kind: "cs", ruleID: "py/unsafe-deserialization", title: "Deserialization of untrusted pickle data", cwe: "CWE-502", severity: "critical",
    description: "A queue consumer unpickles message bodies, so anyone who can publish to the queue can run code in the worker." },
  { kind: "cs", ruleID: "go/weak-crypto-algorithm", title: "Use of a broken cryptographic algorithm", cwe: "CWE-327", severity: "medium",
    description: "Signatures over webhook payloads use HMAC-MD5; collisions make forged payloads cheaper than they should be." },
  { kind: "cs", ruleID: "js/missing-rate-limiting", title: "Expensive route handler without rate limiting", cwe: "CWE-770", severity: "medium",
    description: "The export route renders a full report per request with no rate limit, so a single client can exhaust the pool." },
  { kind: "cs", ruleID: "go/clear-text-logging", title: "Sensitive data written to logs in clear text", cwe: "CWE-312", severity: "medium",
    description: "The request logger records the Authorization header verbatim, putting bearer tokens into the log pipeline." },
  { kind: "cs", ruleID: "java/xxe", title: "XML external entity expansion in document import", cwe: "CWE-611", severity: "high",
    description: "The importer parses uploaded XML with external entities enabled, allowing local file reads and SSRF." },
  { kind: "cs", ruleID: "js/insecure-randomness", title: "Insecure randomness in token generation", cwe: "CWE-338", severity: "medium",
    description: "Password-reset tokens come from Math.random, which is predictable enough to guess outstanding tokens." },
  { kind: "cs", ruleID: "go/open-redirect", title: "Open redirect from user-supplied return URL", cwe: "CWE-601", severity: "low",
    description: "The sign-in flow redirects to the returnTo parameter without checking its host, which helps phishing links look trusted." },
  { kind: "cs", ruleID: "actions/untrusted-checkout", title: "Workflow checks out untrusted pull request code", cwe: "CWE-829", severity: "high",
    description: "A pull_request_target workflow checks out the head ref and runs its scripts with a write token in scope." },
  { kind: "cs", ruleID: "go/incomplete-hostname-regexp", title: "Incomplete hostname regular expression", cwe: "CWE-20", severity: "low",
    description: "An unescaped dot in the allowed-origin pattern matches any character, so lookalike domains pass the check." },
  { kind: "dbot", ruleID: "npm/follow-redirects", title: "follow-redirects leaks the Authorization header across hosts", cwe: "CWE-200", severity: "medium",
    description: "Redirects to a different host keep the original Authorization header, exposing credentials to the redirect target." },
  { kind: "dbot", ruleID: "go/golang.org/x/net", title: "HTTP/2 rapid reset denial of service in golang.org/x/net", cwe: "CWE-400", severity: "high",
    description: "A client can open and cancel streams faster than the server reclaims them, exhausting CPU and memory." },
  { kind: "dbot", ruleID: "npm/semver", title: "Regular expression denial of service in semver range parsing", cwe: "CWE-1333", severity: "medium",
    description: "Crafted version ranges trigger catastrophic backtracking in the range parser." },
  { kind: "dbot", ruleID: "pip/requests", title: "requests leaks Proxy-Authorization on redirect", cwe: "CWE-200", severity: "medium",
    description: "Proxy credentials are re-sent to the destination after an HTTPS redirect." },
  { kind: "dbot", ruleID: "go/github.com/golang-jwt/jwt", title: "JWT library accepts tokens without verifying the algorithm", cwe: "CWE-347", severity: "critical",
    description: "Tokens whose header names an unexpected algorithm are accepted by callers that do not pin one." },
  { kind: "dbot", ruleID: "npm/axios", title: "axios exposes the CSRF token to third-party hosts", cwe: "CWE-352", severity: "medium",
    description: "The XSRF-TOKEN cookie value is attached to requests for every host, not only the page's origin." },
  { kind: "dbot", ruleID: "npm/transitive-vuln", title: "Vulnerable transitive dependency", cwe: "CWE-1104", severity: "low",
    description: "A dependency of a dependency carries a published advisory; whether the vulnerable code is reachable is unknown." },
  { kind: "ccs", ruleID: "s3/public-read-policy", title: "Object storage bucket policy allows public read", cwe: "CWE-284", severity: "high",
    description: "The bucket policy grants object reads to everyone; its contents were meant for the CDN origin only." },
  { kind: "ccs", ruleID: "iam/wildcard-admin", title: "Service account holds a wildcard admin role", cwe: "CWE-269", severity: "critical",
    description: "A workload's service account is bound to an owner-level role across the whole project." },
  { kind: "ccs", ruleID: "sql/public-ip", title: "Database instance reachable on a public IP", cwe: "CWE-668", severity: "high",
    description: "The instance has a public address and an authorised network of 0.0.0.0/0." },
  { kind: "ccs", ruleID: "k8s/privileged-container", title: "Workload runs a privileged container", cwe: "CWE-250", severity: "medium",
    description: "A deployment sets privileged: true, giving the container full access to the node." },
];

const LEVELS: Level[] = ["low", "medium", "high", "critical"];
const RATINGS: Rating[] = ["low", "medium", "high"];
const HOLDS: HoldReason[] = ["breakingChangeAvailable", "lowConfidence", "exceedsAutomatedTurns", "exceedsAutomatedTokens"];
const GHSA = "23456789cfghjmpqrvwx";
const REM_FAILURES = [
  "changeset_too_large: fix spans several packages; exceeds the safe unattended-change budget",
  "error: tests failed after the change and the agent could not repair them",
  "timeout: remediation exceeded its wall-clock budget",
];
const INV_FAILURES = ["timeout: investigation exceeded its wall-clock budget", "error: the agent exited without a report"];

// Where the backlog sits: mostly waiting for investigation or done, a few
// runs in flight (the schedulers cap concurrency).
const REPO_PHASES: [Phase, number][] = [
  ["Opened", 5], ["Enhanced", 24], ["Investigating", 1.5], ["AwaitingApproval", 6], ["Queued", 10],
  ["Remediating", 1], ["InReview", 8], ["Remediated", 18], ["Failed", 4], ["Dismissed", 15], ["HandedOff", 4],
];
// Repo-less findings have nothing to patch, so never reach the queue.
const CLOUD_PHASES: [Phase, number][] = [
  ["Opened", 1], ["Enhanced", 4], ["Investigating", 1], ["HandedOff", 4], ["Dismissed", 3],
];

function pick<T>(rand: () => number, xs: readonly T[]): T {
  return xs[Math.floor(rand() * xs.length)];
}

function weighted(rand: () => number, table: [Phase, number][]): Phase {
  let r = rand() * table.reduce((a, [, w]) => a + w, 0);
  for (const [p, w] of table) if ((r -= w) < 0) return p;
  return table[table.length - 1][0];
}

function between(rand: () => number, lo: number, hi: number): number {
  return lo + rand() * (hi - lo);
}

const iso = (ms: number) => new Date(Math.floor(ms / 1000) * 1000).toISOString().replace(".000Z", "Z");

function hex(rand: () => number, n: number): string {
  let s = "";
  for (let i = 0; i < n; i++) s += Math.floor(rand() * 16).toString(16);
  return s;
}

function ghsa(rand: () => number): string {
  const part = () => Array.from({ length: 4 }, () => pick(rand, [...GHSA])).join("");
  return `GHSA-${part()}-${part()}-${part()}`;
}

// path walks the lifecycle to the target phase; failedFrom says which run
// a Failed finding died in.
function path(target: Phase, failedFrom: "Investigating" | "Remediating"): Phase[] {
  const head: Phase[] = ["Opened", "Enhanced", "Investigating"];
  switch (target) {
    case "Opened":
      return ["Opened"];
    case "Enhanced":
      return ["Opened", "Enhanced"];
    case "Investigating":
      return head;
    case "AwaitingApproval":
    case "Queued":
    case "Dismissed":
    case "HandedOff":
      return [...head, target];
    case "Remediating":
      return [...head, "Queued", "Remediating"];
    case "InReview":
      return [...head, "Queued", "Remediating", "InReview"];
    case "Remediated":
      return [...head, "Queued", "Remediating", "InReview", "Remediated"];
    case "Failed":
      return failedFrom === "Investigating" ? [...head, "Failed"] : [...head, "Queued", "Remediating", "Failed"];
  }
}

// stepTime is how long the finding sat in `from` before moving on.
function stepTime(rand: () => number, from: Phase): number {
  switch (from) {
    case "Opened":
      return between(rand, 60, 66) * MIN; // the accumulation hour, then enhancement
    case "Enhanced":
      return between(rand, 2 * MIN, 36 * HOUR * rand()); // waiting for an investigation slot
    case "Investigating":
      return between(rand, 12, 45) * MIN;
    case "Queued":
      return between(rand, 2 * MIN, 24 * HOUR * rand());
    case "Remediating":
      return between(rand, 15, 50) * MIN;
    case "InReview":
      return between(rand, 2 * HOUR, 5 * DAY);
    default:
      return 0;
  }
}

// dwell is how long the finding has been in its current phase.
function dwell(rand: () => number, phase: Phase): number {
  switch (phase) {
    case "Opened":
      return between(rand, 1, 55) * MIN;
    case "Enhanced":
      return 72 * HOUR * rand() * rand();
    case "Investigating":
    case "Remediating":
      return between(rand, 1, 40) * MIN;
    case "AwaitingApproval":
      return 6 * DAY * rand();
    case "Queued":
      return 3 * DAY * rand();
    case "InReview":
      return 4 * DAY * rand();
    default:
      return 40 * DAY * rand(); // terminal: completed this long ago
  }
}

function severityOf(rand: () => number, base: Level): Level {
  const i = LEVELS.indexOf(base);
  const r = rand();
  const shifted = r < 0.12 ? i - 1 : r > 0.9 ? i + 1 : i;
  return LEVELS[Math.max(0, Math.min(LEVELS.length - 1, shifted))];
}

function backlogFinding(rand: () => number, repos: string[], taken: Set<string>): Finding {
  const rule = pick(rand, RULES);
  const severity = severityOf(rand, rule.severity);
  const cloud = rule.kind === "ccs";
  // A skewed pick, so a handful of repositories carry most findings.
  const repo = cloud ? undefined : repos[Math.floor(Math.pow(rand(), 2.2) * repos.length)];
  const short = repo ? repo.slice(repo.indexOf("/") + 1) : `${pick(rand, ["prod", "staging", "shared"])}-${rule.ruleID.split("/")[0]}`;
  const team = `${short.split("-")[cloud ? 1 : 0]}-team`;
  let name: string;
  do name = `${rule.kind === "cs" ? "gh-cs" : rule.kind}-${short}-${hex(rand, 6)}`;
  while (taken.has(name));
  taken.add(name);

  const phase = weighted(rand, cloud ? CLOUD_PHASES : REPO_PHASES);
  const failedFrom = rand() < 0.35 ? "Investigating" : "Remediating";
  const steps = path(phase, failedFrom);
  const gaps = steps.slice(0, -1).map((p) => stepTime(rand, p));
  let at = NOW - dwell(rand, phase) - gaps.reduce((a, b) => a + b, 0);
  const phaseTimes: PhaseTime[] = steps.map((p, i) => {
    const pt = { phase: p, at: iso(at) };
    at += gaps[i] ?? 0;
    return pt;
  });
  const when = (p: Phase) => phaseTimes.find((pt) => pt.phase === p)?.at;
  const left = (p: Phase) => phaseTimes[phaseTimes.findIndex((pt) => pt.phase === p) + 1]?.at;
  const first = phaseTimes[0].at;
  const terminal = phase === "Remediated" || phase === "Dismissed" || phase === "HandedOff" || phase === "Failed";

  const repoURL = repo && `https://github.com/${repo}`;
  const issue = 1 + Math.floor(rand() * 900);
  const alertNo = 1 + Math.floor(rand() * 600);
  const f: Finding = {
    name,
    createdAt: first,
    integration: cloud ? "cloud-bitwise" : "github-bitwise",
    source: cloud ? "cloud-config-scan" : rule.kind === "dbot" ? "dependabot" : "github-code-scanning",
    ...(repo && { repository: { type: "github" as const, url: repoURL!, name: repo, defaultBranch: "main" } }),
    advisories:
      rule.kind === "dbot"
        ? [ghsa(rand), `CVE-2026-${10_000 + Math.floor(rand() * 40_000)}`, rule.cwe]
        : rand() < 0.4 && !cloud
          ? [`CVE-2026-${10_000 + Math.floor(rand() * 40_000)}`, rule.cwe]
          : [rule.cwe],
    ruleID: rule.ruleID,
    title: rule.title,
    description: rule.description,
    severity,
    alerts: [
      cloud
        ? { id: `ccs/${alertNo + 9000}` }
        : {
            id: `alerts/${alertNo}`,
            url: `${repoURL}/security/${rule.kind === "dbot" ? "dependabot" : "code-scanning"}/${alertNo}`,
          },
    ],
    phase,
    phaseTimes,
    firstObservedAt: first,
    tracking: {
      issueNumber: issue,
      url: `https://github.com/${repo ?? "bitwise-media-group/infrastructure"}/issues/${issue}`,
      state: phase === "Remediated" || phase === "Dismissed" ? "closed" : "open",
    },
  };
  if (phase === "Opened") {
    f.accumulateUntil = iso(Date.parse(first) + HOUR);
    return f;
  }
  f.owners = [team];
  f.priority = severity;
  if (!terminal && rand() < 0.03) f.suspend = true;
  if ((phase === "Enhanced" || phase === "Queued") && rand() < 0.02) {
    f.expedite = { by: "platform-oncall", at: iso(NOW - rand() * HOUR) };
  }

  const invAttempts = phase === "Failed" && failedFrom === "Investigating" ? 2 + Math.floor(rand() * 2) : 1;
  if (phase === "Investigating") {
    f.attempts = { investigation: 1 };
    f.activeRun = { kind: "investigation", name: `${name}-inv-1` };
    return f;
  }
  if (phase === "Enhanced") return f;

  // Every later phase carries the investigation's verdict.
  const recommendation: Recommendation =
    phase === "Dismissed" ? "ignore" : phase === "HandedOff" ? "manual" : "remediate";
  const inv: InvestigationSummary = {
    name: `${name}-inv-${invAttempts}`,
    attempt: invAttempts,
    outcome: phase === "Failed" && failedFrom === "Investigating" ? pick(rand, ["timeout", "error"]) : "ok",
    completedAt: left("Investigating"),
  };
  if (inv.outcome === "ok") {
    Object.assign(inv, {
      recommendation,
      confidence: between(rand, 0.72, 0.98).toFixed(2),
      exploitability: pick(rand, RATINGS),
      likelihood: pick(rand, RATINGS),
      impact: pick(rand, RATINGS),
    });
  }
  if (phase === "AwaitingApproval") {
    inv.awaitApproval = true;
    inv.holdReasons = [pick(rand, HOLDS)];
    inv.estimate = { maxTurns: 60 + Math.floor(rand() * 120), tokenBudget: 100_000 + Math.floor(rand() * 200_000) };
  }
  f.investigation = inv;
  f.attempts = { investigation: invAttempts };
  if (phase === "Failed" && failedFrom === "Investigating") {
    f.lastFailureReason = pick(rand, INV_FAILURES);
  }

  if (phase === "Remediating") {
    f.attempts.remediation = 1;
    f.activeRun = { kind: "remediation", name: `${name}-rem-1` };
  }
  if (phase === "InReview" || phase === "Remediated" || (phase === "Failed" && failedFrom === "Remediating")) {
    const remAttempts = phase === "Failed" ? 1 + Math.floor(rand() * 3) : 1;
    const ok = phase !== "Failed";
    const rem: RemediationSummary = {
      name: `${name}-rem-${remAttempts}`,
      attempt: remAttempts,
      outcome: ok ? "ok" : pick(rand, ["changeset_too_large", "error", "timeout"]),
      success: ok,
      completedAt: left("Remediating"),
    };
    if (ok) rem.branch = `patch/security-${issue}`;
    f.remediation = rem;
    f.attempts.remediation = remAttempts;
    if (!ok) f.lastFailureReason = pick(rand, REM_FAILURES);
  }
  if (phase === "InReview" || phase === "Remediated") {
    const number = 100 + Math.floor(rand() * 2000);
    const merged = when("Remediated");
    f.pullRequest = {
      number,
      url: `${repoURL}/pull/${number}`,
      state: merged ? "merged" : "open",
      ...(merged && { mergedAt: merged }),
    };
  }
  if (terminal) f.completedAt = phaseTimes[phaseTimes.length - 1].at;
  return f;
}

// backlog returns BACKLOG_FINDINGS generated findings whose names avoid the
// hand-written ones; the same on every load.
export function backlog(existing: Finding[]): Finding[] {
  const rand = prng(0xba5e);
  const repos = tailNames(prng(0x5eed), 300, new Set());
  const taken = new Set(existing.map((f) => f.name));
  return Array.from({ length: BACKLOG_FINDINGS }, () => backlogFinding(rand, repos, taken));
}
