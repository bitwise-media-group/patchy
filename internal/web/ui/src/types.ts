// Mirrors the status-server payload (future patchy internal/web/data.go).
// Field names match the Finding / FindingRollup CRD json tags in
// patchy api/v1alpha1; optional Go pointers become optional fields here.

// Phase is the finding lifecycle position (api/v1alpha1/transitions.go).
export type Phase =
  | "Opened"
  | "Enhanced"
  | "Investigating"
  | "AwaitingApproval"
  | "Queued"
  | "Remediating"
  | "InReview"
  | "Remediated"
  | "Failed"
  | "Dismissed"
  | "HandedOff";

// PHASE_ORDER is the happy path plus the approval detour, in rail order.
export const PHASE_ORDER: Phase[] = [
  "Opened",
  "Enhanced",
  "Investigating",
  "AwaitingApproval",
  "Queued",
  "Remediating",
  "InReview",
  "Remediated",
];

// TERMINAL_PHASES mirrors transitions.go; Dismissed and HandedOff are
// revivable terminals (issue reopen / approve).
export const TERMINAL_PHASES: ReadonlySet<Phase> = new Set([
  "Remediated",
  "Failed",
  "Dismissed",
  "HandedOff",
]);

export type Level = "low" | "medium" | "high" | "critical";
export type Rating = "none" | "low" | "medium" | "high" | "critical";
export type Recommendation = "remediate" | "ignore" | "manual";

// ActionVerb is a user action on a finding. The server resolves which verbs
// the requesting user may invoke (per-finding, from their authorization) and
// injects them as Finding.userActions; the UI never decides authorization.
export type ActionVerb = "approve" | "suspend" | "resume";

export interface Location {
  path: string;
  startLine?: number;
  endLine?: number;
  snippet?: string;
}

export interface Alert {
  id: string;
  url?: string;
  locations?: Location[];
}

export interface FindingRepository {
  type: "github";
  url: string;
  name?: string; // "owner/repo"
  defaultBranch?: string;
}

export interface RelatedFinding {
  name: string;
  relationship: "duplicate-of" | "successor-of" | "related-to";
}

export interface Approval {
  by: string;
  at: string;
  note?: string;
}

export interface PhaseTime {
  phase: Phase;
  at: string;
}

export interface TrackingStatus {
  issueNumber?: number;
  url?: string;
  state?: string;
}

export interface Enrichment {
  enhancer: string;
  owners?: string[];
  markdown?: string;
  appliedAt?: string;
}

export interface InvestigationSummary {
  name?: string;
  attempt?: number;
  outcome?: string;
  recommendation?: Recommendation;
  confidence?: string; // decimal string in [0,1], like the CRD
  exploitability?: Rating;
  likelihood?: Rating;
  impact?: Rating;
  awaitApproval?: boolean;
  completedAt?: string;
}

export interface RemediationSummary {
  name?: string;
  attempt?: number;
  outcome?: string;
  success?: boolean;
  branch?: string;
  completedAt?: string;
}

export interface PullRequestStatus {
  number: number;
  url?: string;
  state?: "open" | "merged" | "closed";
  mergedAt?: string;
}

export interface AttemptCounts {
  investigation?: number;
  remediation?: number;
}

export interface ActiveRun {
  kind: "investigation" | "remediation";
  name: string;
}

// Finding is the flattened per-finding projection: metadata + spec + status
// plus the requesting user's permitted verbs.
export interface Finding {
  name: string; // metadata.name
  createdAt?: string;
  // spec
  integration?: string;
  source?: string;
  repository?: FindingRepository; // absent for repo-less (e.g. cloud) findings
  advisories: string[]; // [0] is authoritative (GHSA > CVE > CWE)
  ruleID?: string;
  title?: string;
  description?: string;
  severity?: Level;
  alerts?: Alert[];
  overflowAlerts?: number;
  related?: RelatedFinding[];
  suspend?: boolean;
  approval?: Approval;
  // status
  phase?: Phase;
  phaseTimes?: PhaseTime[];
  firstObservedAt?: string;
  accumulateUntil?: string;
  tracking?: TrackingStatus;
  owners?: string[];
  enrichments?: Enrichment[];
  priority?: Level;
  investigation?: InvestigationSummary;
  remediation?: RemediationSummary;
  pullRequest?: PullRequestStatus;
  attempts?: AttemptCounts;
  activeRun?: ActiveRun;
  lastFailureReason?: string;
  completedAt?: string;
  // authorization: verbs the requesting user may invoke on this finding.
  // Empty or absent means read-only; the action bar does not render.
  userActions?: ActionVerb[];
}

// ---- Rollups (api/v1alpha1/findingrollup_types.go) ----
//
// Rollups are sharded in-cluster: one object per scope value. The payload
// carries only scope + status; rows are identified by scope.key ("" = total),
// never by object name (repository object names are sanitized hashes).

export type ScopeType = "total" | "repository" | "harness" | "model";

export interface RollupScope {
  type: ScopeType;
  key?: string; // "" for total, "owner/repo", harness ID, model ID
}

// StageAggregate accumulates one stage's runs. Averages and rates are
// computed client-side from sum ÷ count.
export interface StageAggregate {
  runs?: number;
  succeeded?: number;
  outcomes?: Record<string, number>;
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  costMicroUSD?: number;
  elapsedMilliseconds?: number;
}

// RollupBucket: total and repository scopes carry everything; harness and
// model scopes carry only stages (a finding has no single harness/model
// owner) — their findings count is legitimately absent, not zero.
export interface RollupBucket {
  findings?: number;
  phases?: Record<string, number>; // terminal phases: remediated, failed, …
  recommendations?: Record<string, number>;
  attempts?: number;
  stages?: Record<string, StageAggregate>; // "investigation" | "remediation"
}

export interface MonthlyBucket {
  findings?: number;
  runs?: number;
  costMicroUSD?: number;
}

export interface Rollup {
  scope: RollupScope;
  firstProcessed?: string;
  lastProcessed?: string;
  bucket: RollupBucket;
  monthly?: Record<string, MonthlyBucket>; // total scope only, keyed "2026-07"
}

// DatasetUser is the signed-in identity the top bar renders. Absent when
// unauthenticated; loggedIn is false for fixed-identity auth modes that have
// nothing to sign out of.
export interface DatasetUser {
  name: string;
  loggedIn: boolean;
}

// Dataset is the payload behind GET /api/findings (everything) and
// GET /api/rollups (findings empty, no user — the always-public statistics
// surface). One flat list per concern; all filtering, sorting, and
// derivation is client-side so the server stays a thin read-only projection.
export interface Dataset {
  generatedAt: string;
  namespace?: string;
  version?: string;
  user?: DatasetUser;
  findings: Finding[];
  rollups?: Rollup[];
}
