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
export type ActionVerb = "approve" | "retry" | "expedite" | "suspend" | "resume";

// AdminVerb is a namespace-wide action in the user menu (demo tooling):
// replay redelivers the webhook delivery log, reset deletes all pipeline
// state. Granted verbs arrive as DatasetUser.adminActions.
export type AdminVerb = "replay" | "reset";

// IntegrationVerb is an integration-scoped action (RBAC verbs on
// integrations): backfill lists pre-existing open alerts into findings;
// replay/reset are the demo tooling. Granted verbs arrive as
// DatasetUser.integrationActions.
export type IntegrationVerb = "backfill" | "replay" | "reset";

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

// FindingCloudResource identifies the cloud resource an infrastructure
// finding was raised against.
export interface FindingCloudResource {
  provider: "google";
  name: string; // the platform's canonical resource identifier
  type?: string; // e.g. "google.cloud.storage.Bucket"
  project?: string;
  location?: string;
  displayName?: string;
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

// ActionRequest is a recorded human retry/expedite request.
export interface ActionRequest {
  by: string;
  at: string;
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
  attributes?: Record<string, string>;
  markdown?: string;
  appliedAt?: string;
}

// Usage is token and cost accounting — one run's, or the finding's total
// across every attempt of both stages. Cost is integer micro-USD, matching
// StageAggregate.
export interface Usage {
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  costMicroUSD?: number;
}

// HoldReason is why a finding stopped in AwaitingApproval instead of queueing.
export type HoldReason =
  | 'breakingChangeAvailable'
  | 'lowConfidence'
  | 'exceedsAutomatedTurns'
  | 'exceedsAutomatedTokens';

// Estimate is an investigation's prediction of a remediation's cost. It never
// limits the run — see Budget.
export interface Estimate {
  maxTurns?: number;
  tokenBudget?: number;
}

// Budget is a remediation's predicted-vs-granted budget. What it actually
// spent is the run's own numTurns and usage.outputTokens, so the three-way
// comparison is estimated -> granted -> actual.
export interface Budget {
  estimated?: Estimate;
  grantedMaxTurns?: number;
  grantedTokenBudget?: number;
}

// InvestigationSummary mirrors the finding's own investigation status —
// the bounded verdict fields the list rows render.
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
  holdReasons?: HoldReason[];
  estimate?: Estimate; // what it predicted the remediation would cost
  completedAt?: string;
}

// InvestigationDetail adds what the detail route lifts from the
// Investigation child (absent once it expires).
export interface InvestigationDetail extends InvestigationSummary {
  report?: string;
  harness?: string;
  model?: string;
  numTurns?: number;
  usage?: Usage;
  sessionID?: string;
  transcript?: TranscriptSummary;
}

// RemediationSummary mirrors the finding's own remediation status.
export interface RemediationSummary {
  name?: string;
  attempt?: number;
  outcome?: string;
  success?: boolean;
  branch?: string;
  completedAt?: string;
}

// RemediationDetail adds what the detail route lifts from the Remediation
// child (absent once it expires).
export interface RemediationDetail extends RemediationSummary {
  report?: string;
  harness?: string;
  model?: string;
  numTurns?: number;
  budget?: Budget;
  usage?: Usage;
  sessionID?: string;
  transcript?: TranscriptSummary;
}

// TranscriptSummary says whether a run has a captured conversation worth
// opening; the turns themselves stream from the transcript endpoint.
export interface TranscriptSummary {
  turns: number;
  truncated?: boolean;
}

export type TurnRole = "assistant" | "user" | "system";
export type TurnKind = "text" | "thinking" | "tool_use" | "tool_result" | "notice";

// TranscriptTurn is one entry of an agent's conversation, normalised across
// harnesses. Mirrors internal/web.Turn; keep the two in lockstep.
export interface TranscriptTurn {
  seq: number;
  at?: string;
  role: TurnRole;
  kind: TurnKind;
  tool?: string;
  text?: string;
  truncated?: boolean;
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

// FindingSummary is the trimmed list row GET /api/findings ships: what the
// findings table, filters, and stat tiles render, and nothing whose size is
// unbounded (description, alert snippets, enrichment markdown, run reports —
// those come from the per-finding detail route when a finding is opened).
export interface FindingSummary {
  name: string; // metadata.name
  createdAt?: string;
  // spec
  integration?: string;
  source?: string;
  repository?: FindingRepository; // absent for repo-less (e.g. cloud) findings
  // Set when the finding is about infrastructure rather than repository code.
  // A finding with a cloudResource and no repository is one whose resource
  // carried no ownership labels to resolve a repository from.
  cloudResource?: FindingCloudResource;
  advisories: string[]; // [0] is authoritative (GHSA > CVE > CWE)
  ruleID?: string;
  title?: string;
  severity?: Level;
  suspend?: boolean;
  // status
  phase?: Phase;
  firstObservedAt?: string;
  accumulateUntil?: string;
  owners?: string[];
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

// Finding is the full per-finding projection behind GET /api/findings/{name}:
// the summary plus the unbounded detail, with the run summaries widened to
// carry the lifted child fields (report markdown, usage, transcript).
export interface Finding extends FindingSummary {
  description?: string;
  alerts?: Alert[];
  overflowAlerts?: number;
  related?: RelatedFinding[];
  approval?: Approval;
  retry?: ActionRequest;
  expedite?: ActionRequest;
  phaseTimes?: PhaseTime[];
  tracking?: TrackingStatus;
  enrichments?: Enrichment[];
  investigation?: InvestigationDetail;
  remediation?: RemediationDetail;
  totalUsage?: Usage; // summed across every attempt of both stages
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
  turns?: number;
  estimate?: EstimateAggregate;
}

// EstimateAggregate is predicted-against-actual cost over the runs that
// carried an estimate. Both sides cover the same runs, so the skew
// (actual / predicted - 1) is like-for-like.
export interface EstimateAggregate {
  runs?: number;
  predictedTurns?: number;
  actualTurns?: number;
  predictedOutputTokens?: number;
  actualOutputTokens?: number;
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
  // Granted namespace-wide verbs the user menu renders; absent means none.
  adminActions?: AdminVerb[];
  // Whether the configuration view is reachable (native get on
  // integrations); the nav link renders only when true.
  configView?: boolean;
  // Granted integration-scoped verbs the configuration view's triggers
  // check; absent means none.
  integrationActions?: IntegrationVerb[];
}

// Dataset is the payload behind GET /api/findings (trimmed summaries) and
// GET /api/rollups (findings empty, no user — the always-public statistics
// surface). One flat list per concern; all filtering, sorting, and
// derivation is client-side so the server stays a thin read-only projection.
export interface Dataset {
  generatedAt: string;
  namespace?: string;
  version?: string;
  user?: DatasetUser;
  findings: FindingSummary[];
  rollups?: Rollup[];
}

// OfflineDataset is a Dataset whose findings are full: what the demo kit and
// the embedded snapshot carry, since neither has a detail route to lazily
// fetch from.
export interface OfflineDataset extends Dataset {
  findings: Finding[];
}

// ---- Configuration view (internal/web/config.go) ----
//
// ConfigDataset is the payload behind GET /api/config: the configured
// Forges, Integrations and enhancers. Never public — the route requires a
// signed-in identity holding native get on integrations.

// ForgeConfig is one Forge's configuration surface.
export interface ForgeConfig {
  name: string;
  provider: string;
  baseURL?: string;
  orgs?: string[];
  repositories?: string[];
  secretRef?: string;
  interval?: string;
  suspend?: boolean;
  // Ready condition status ("True" | "False" | "Unknown"); absent until
  // the controller reports.
  ready?: string;
  readyMessage?: string;
}

// RedeliveryStatus mirrors an Integration's status.redelivery.
export interface RedeliveryStatus {
  lastSweepAt?: string;
  scanned?: number;
  redelivered?: number;
  truncated?: boolean;
  error?: string;
}

// BackfillStatus mirrors an Integration's status.backfill plus the pending
// spec.backfill echo the trigger renders.
export interface BackfillStatus {
  lastRunAt?: string;
  listed?: number;
  ingested?: number;
  truncated?: boolean;
  error?: string;
  requestedBy?: string;
  requestedAt?: string;
  // A request the controller has not consumed yet.
  pending?: boolean;
}

// IntegrationConfig is one Integration's configuration surface, including
// the backfill trigger's state.
export interface IntegrationConfig {
  name: string;
  provider: string;
  webhookPath?: string;
  secretRef?: string;
  interval?: string;
  suspend?: boolean;
  ready?: string;
  readyMessage?: string;
  capabilities?: string[];
  redelivery?: RedeliveryStatus;
  backfill?: BackfillStatus;
  // Whether the backfill trigger can do anything here (github provider
  // with code scanning enabled).
  backfillSupported?: boolean;
}

// EnhancerConfig is one context-enhancer instance derived from the
// Integrations (the static-context enhancer is a controller flag, invisible
// here).
export interface EnhancerConfig {
  id: string;
  integration?: string;
  enabled: boolean;
  // A singleton capability enabled by more than one Integration — a
  // configuration error the view surfaces rather than resolving.
  ambiguous?: boolean;
}

export interface ConfigDataset {
  generatedAt: string;
  namespace?: string;
  forges: ForgeConfig[];
  integrations: IntegrationConfig[];
  enhancers: EnhancerConfig[];
}
