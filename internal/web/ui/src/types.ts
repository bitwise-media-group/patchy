// Mirrors the status-server payload (internal/web/data.go).
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

// MaintainedVerb is a scoped maintainer verb (internal/action
// MaintainedVerbs): the base verb, allowed only on repositories the user's
// linked GitHub identity administers or maintains.
export type MaintainedVerb = "backfill-maintained" | "approve-maintained" | "expedite-maintained";

// GitHubLink mirrors internal/web.GitHubLink: one github Integration the
// user may link a GitHub account through, and whether they have.
export interface GitHubLink {
  integration: string;
  host: string;
  login?: string;
  connected: boolean;
}

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

// Usage is token and cost accounting for one run, or the finding's total
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
  | 'exceedsAutomatedTokens'
  | 'secretRotation';

// Estimate is an investigation's prediction of a remediation's cost. It never
// limits the run; see Budget.
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

// InvestigationSummary mirrors the finding's own investigation status: the
// bounded verdict fields the list rows render.
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
  // reportArchived: the full, untruncated report is in the run archive.
  reportArchived?: boolean;
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
  // reportArchived: the full, untruncated report is in the run archive.
  reportArchived?: boolean;
  // patchAvailable: the change is in the run archive as a `git am`-able patch.
  patchAvailable?: boolean;
}

// TranscriptSummary says whether a run has a captured conversation worth
// opening; the turns themselves stream from the transcript endpoint.
export interface TranscriptSummary {
  turns: number;
  truncated?: boolean;
  // archived: the conversation is in the run archive; the client plays it
  // back from a presigned URL instead of the transcript stream.
  archived?: boolean;
}

// ArchiveObject names what the archive route can presign for one run.
export type ArchiveObject = "transcript" | "report" | "patch" | "manifest";

// PresignedURL is a short-lived URL the browser reads straight from the
// archive bucket. Mirrors internal/web.PresignedURL.
export interface PresignedURL {
  url: string;
  expiresAt: string;
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
  // id pairs a tool_use turn with its tool_result.
  id?: string;
  // path is the file a tool call targets.
  path?: string;
  // syntax hints how to render text; absent means infer from path.
  syntax?: TurnSyntax;
  text?: string;
  truncated?: boolean;
}

export type TurnSyntax = "diff" | "shell";

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
// unbounded (description, alert snippets, enrichment markdown, run reports).
// Those come from the per-finding detail route when a finding is opened.
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
// owner), so their findings count is absent, not zero.
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
  // Granted scoped maintainer verbs; absent means none. The user menu hints
  // "Connect GitHub" when these are held with no link.
  maintainedActions?: MaintainedVerb[];
  // The Integrations the user may link GitHub through; absent when linking
  // is off.
  github?: GitHubLink[];
}

// Dataset is the payload behind GET /api/rollups: the metrics summary, plus
// the namespace, version and user the top bar renders. The summary carries
// the total, model and harness shards and only the top repositories; the
// rest page through GET /api/rollups/repositories (RepositoryPage). The
// findings list is FindingsPage (GET /api/findings), which carries the same
// header, so either can stand in for the top bar.
export interface DatasetHeader {
  generatedAt: string;
  namespace?: string;
  version?: string;
  user?: DatasetUser;
}

export interface Dataset extends DatasetHeader {
  rollups?: Rollup[];
  // How many repository shards exist in all (summary only).
  repositoryCount?: number;
}

// FindingsQuery is the query string of GET /api/findings and
// GET /api/findings/suggest (internal/web/findingquery.go). The repeatable
// filters match any of their values; every set filter must match.
export interface FindingsQuery {
  phase?: Phase[];
  severity?: Level[];
  verdict?: Recommendation[];
  // Repository terms match any of their values: exact "owner/name" names,
  // and case-insensitive prefixes and substrings of the name (of the whole
  // "owner/name" when the term holds a "/").
  repo?: string[];
  repoPrefix?: string[];
  repoContains?: string[];
  q?: string; // case-insensitive text search
  limit?: number; // server caps at 200 (20 for suggest)
  cursor?: string; // a previous page's nextCursor
}

// RepositoryCount is one repository and how many findings name it.
export interface RepositoryCount {
  name: string;
  findings: number;
}

// FindingFacets counts every finding, whatever the filters: the phase rail
// and the filter's tallies. A finding without a value counts in none.
export interface FindingFacets {
  total: number;
  open: number;
  running: number;
  suspended: number;
  phases?: Partial<Record<Phase, number>>;
  severities?: Partial<Record<Level, number>>;
  verdicts?: Partial<Record<Recommendation, number>>;
  // The repositories with most findings, heaviest first.
  repositories?: RepositoryCount[];
  repositoryCount: number;
}

// FindingsPage is one page of GET /api/findings, most severe first. The
// facets come on the first page only (no cursor).
export interface FindingsPage extends DatasetHeader {
  findings: FindingSummary[];
  matched: number; // findings matching the filters, across every page
  nextCursor?: string; // absent on the last page
  facets?: FindingFacets;
}

// FindingSuggestions is GET /api/findings/suggest: the findings filter's
// type-ahead for one typed term.
export interface FindingSuggestions {
  // Findings the filters match with the term as the text search.
  matched: number;
  // Repositories (filters aside) whose name contains the term, best first.
  repositories: RepositoryCount[];
  repositoryTotal: number;
  // The term's reach as a repository prefix and substring filter.
  prefix: RepositoryMatch;
  contains: RepositoryMatch;
}

// RepositoryMatch is how many repositories a repository-name term matches,
// and how many findings name them.
export interface RepositoryMatch {
  repositories: number;
  findings: number;
}

// RepositorySort orders GET /api/rollups/repositories. "relevance" is the
// type-ahead ranking and needs q.
export type RepositorySort =
  | "relevance"
  | "findings"
  | "remediated"
  | "failed"
  | "spend"
  | "costPerRemediation"
  | "name";

// RepositoryQuery is the query string of GET /api/rollups/repositories
// (internal/web/rollupquery.go).
export interface RepositoryQuery {
  q?: string; // case-insensitive name match
  key?: string; // one exact repository (adds rank)
  sort?: RepositorySort;
  order?: "asc" | "desc";
  offset?: number;
  limit?: number; // server caps at 100
  aggregate?: boolean; // sum every match into `aggregate`
}

// RepositoryPage is one page of repository shards.
export interface RepositoryPage {
  total: number; // matches
  of: number; // repository shards in all
  offset: number;
  repositories: Rollup[];
  aggregate?: Rollup;
  rank?: number; // 1-based by findings, for a key lookup
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
// Forges, Integrations and enhancers. Never public: the route requires a
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

// BackfillStatus is the Integration's latest BackfillRequest (requests
// still awaiting an operator are skipped), as the trigger renders it.
export interface BackfillStatus {
  // The BackfillRequest's name, and its phase.
  request?: string;
  phase?: BackfillPhase;
  lastRunAt?: string;
  listed?: number;
  ingested?: number;
  truncated?: boolean;
  error?: string;
  requestedBy?: string;
  requestedAt?: string;
  // A request the controller has not attempted yet (a failed attempt clears
  // it, so the trigger re-enables for a corrected request).
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
  // A singleton capability enabled by more than one Integration: a
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

// ---- Repositories page (internal/web/repositories.go) ----

// RepositoriesPayload is GET /api/repositories: the tracked repositories'
// finding counts, rollups and backfill state.
export interface RepositoriesPayload {
  generatedAt: string;
  repositories: RepositorySummary[];
  // Whether the caller holds the backfill grant: the backfill runs at once.
  canBackfill: boolean;
  // Whether the caller holds backfill-maintained (and GitHub linking is
  // on): with a connected GitHub account, the backfill runs at once on
  // repositories they administer or maintain.
  canBackfillMaintained?: boolean;
  // Whether the caller may file a request an operator decides.
  canRequestBackfill: boolean;
  // How long after a repository's backfill completed another is refused.
  cooldownSeconds: number;
  // The Integrations the caller may link GitHub through; absent when
  // linking is off.
  github?: GitHubLink[];
  // Whether the integration-controller's access check is configured.
  accessCheck: boolean;
}

export interface RepositorySummary {
  name: string;
  findings: number;
  phases?: Partial<Record<Phase, number>>;
  rollup?: { findings: number; lastProcessed?: string };
  backfill?: RepositoryBackfill;
  // When the cooldown lets the repository be backfilled again (RFC 3339);
  // absent when it may be now.
  backfillAvailableAt?: string;
}

// BackfillPhase mirrors v1alpha1.BackfillRequestPhase.
export type BackfillPhase = "AwaitingApproval" | "Pending" | "Complete" | "Failed" | "Denied";

// BackfillVia is how a request was authorized: the backfill grant, a
// maintainer's GitHub role, or an operator's decision on a request.
export type BackfillVia = "backfill" | "maintained" | "requested";

// RepositoryBackfill is the backfill request that best describes a
// repository: an open one over a finished one, then the newest. The run
// counts cover the whole request, not this repository alone.
export interface RepositoryBackfill {
  request?: string;
  integration: string;
  requestedBy?: string;
  requestedAt?: string;
  phase?: BackfillPhase;
  via?: BackfillVia;
  // An open request: not walked yet, or waiting for an operator.
  pending?: boolean;
  // An operator's note on a decided request.
  decisionReason?: string;
  lastRunAt?: string;
  listed?: number;
  ingested?: number;
  error?: string;
}

// AccessStatus mirrors internal/repoaccess.Status.
export type AccessStatus = "ok" | "not_installed" | "forbidden" | "not_found" | "no_analysis" | "error";

export interface AccessCheck {
  status: AccessStatus;
  detail?: string;
}

export interface IntegrationAccess {
  name: string;
  alerts: { codeScanning?: AccessCheck; secretScanning?: AccessCheck };
}

// ForgeStatus mirrors internal/repoaccess.ForgeStatus.
export type ForgeStatus = "ok" | "missing" | "ambiguous" | "no_write" | "error";

export interface ForgeAccess {
  name?: string;
  status: ForgeStatus;
  contents?: "none" | "read" | "write" | "unknown";
  detail?: string;
}

// RepositoryAccess is one repository's verdict from GET
// /api/repositories/access.
export interface RepositoryAccess {
  name: string;
  // "known": patchy has seen the repository, so it was not probed.
  skipped?: "known";
  integrations?: IntegrationAccess[];
  forge?: ForgeAccess;
  // The integration a backfill would be stamped on; absent when none can
  // read the alerts.
  backfillIntegration?: string;
}

export interface RepositoryAccessPayload {
  available: boolean;
  error?: string;
  repositories: RepositoryAccess[];
}

// BackfillOutcome is what a one-repository backfill POST did: a new
// request that runs at once, a new request an operator decides, or an open
// request that already covers the repository.
export type BackfillOutcome = "requested" | "awaiting-approval" | "covered";

// RepositoryBackfillResult answers POST /api/repositories/backfill.
export interface RepositoryBackfillResult {
  integration: string;
  // The BackfillRequest created, or the open one covering the repository.
  request?: string;
  outcome: BackfillOutcome;
}

// ---- Backfill requests (internal/web/backfillrequests.go) ----

// BackfillDecision is an operator's verdict on a requested backfill.
export interface BackfillDecision {
  verdict: "Approved" | "Denied";
  by: string;
  at: string;
  reason?: string;
}

// BackfillRequest mirrors internal/web.BackfillRequestWire.
export interface BackfillRequest {
  name: string;
  integration: string;
  requestedBy: string;
  requestedAt?: string;
  // Absent: everything the Integration can see.
  repositories?: string[];
  via: BackfillVia;
  githubLogin?: string;
  phase?: BackfillPhase;
  attempts?: number;
  lastRunAt?: string;
  completedAt?: string;
  listed?: number;
  ingested?: number;
  truncated?: boolean;
  error?: string;
  decision?: BackfillDecision;
}

// BackfillRequestsPayload is GET /api/backfill-requests: every request,
// newest first, and whether the caller may approve or deny.
export interface BackfillRequestsPayload {
  generatedAt: string;
  requests: BackfillRequest[];
  canDecide: boolean;
}

// ---- GitHub link (internal/web/github.go) ----

// GitHubRepository is one repository the user administers or maintains.
export interface GitHubRepository {
  name: string; // "owner/name"
  role: string; // "admin" | "maintain"
}

// GitHubRepositoriesPayload is GET /api/github/repositories?integration=.
export interface GitHubRepositoriesPayload {
  integration: string;
  login: string;
  repositories: GitHubRepository[];
  truncated?: boolean;
}
