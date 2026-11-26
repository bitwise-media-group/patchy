// Data access: the embedded snapshot (offline), demo mode (the built-in mock
// data, flagged by window.__PATCHY_STATUS_DEMO__), or the live API plus the
// SSE refetch signal.
//
// Authorization is entirely server-side: the payload arrives with each
// finding's permitted verbs already resolved for the requesting user, and a
// mutation is re-checked on POST. The client only reflects 401/403.

import type {
  ActionVerb,
  AdminVerb,
  BackfillRequestsPayload,
  ConfigDataset,
  Dataset,
  DatasetHeader,
  Finding,
  FindingSuggestions,
  FindingsPage,
  FindingsQuery,
  GitHubRepositoriesPayload,
  OfflineDataset,
  RepositoriesPayload,
  RepositoryAccessPayload,
  RepositoryBackfillResult,
  RepositoryPage,
  RepositoryQuery,
  ArchiveObject,
  PresignedURL,
  Rollup,
  TranscriptTurn,
} from "./types";
import { queryRepositoriesLocal, summarizeRollups } from "./rollups";
import { queryFindingsLocal, queryParams, suggestFindingsLocal } from "./filters";
import { availableActions, retryTarget } from "./actions";
import { mockConfig } from "./mock/config";
import { mockArchiveURL, mockDataset, mockTranscript } from "./mock/findings";
import { DEFAULT_PERSONA, type Persona } from "./mock/personas";
import {
  MockStatusError,
  mockBackfillDecision,
  mockBackfillRequests,
  mockGitHubLink,
  mockGitHubRepositories,
  mockRepositories,
  mockRepositoryAccess,
  mockRepositoryBackfill,
  mockUserActions,
} from "./mock/repositories";
import { githubConnectHref } from "./repositories";

// AuthRequiredError: the server wants a signed-in user (HTTP 401).
export class AuthRequiredError extends Error {
  constructor() {
    super("authentication required");
  }
}

// ForbiddenError: the user is signed in but lacks the verb (HTTP 403).
export class ForbiddenError extends Error {}

export interface SnapshotPayload {
  dataset: OfflineDataset;
}

declare global {
  interface Window {
    __PATCHY_STATUS_SNAPSHOT__?: SnapshotPayload;
    __PATCHY_STATUS_DEMO__?: boolean;
  }
}

export type DataMode = "snapshot" | "demo" | "live";

export function dataMode(): DataMode {
  if (window.__PATCHY_STATUS_SNAPSHOT__?.dataset) return "snapshot";
  if (window.__PATCHY_STATUS_DEMO__) return "demo";
  return "live";
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init);
  if (res.status === 401) throw new AuthRequiredError();
  if (res.status === 403) {
    const text = (await res.text()).trim();
    throw new ForbiddenError(text || "Permission denied.");
  }
  if (!res.ok) throw new Error(`${path}: HTTP ${res.status}`);
  return (await res.json()) as T;
}

// requestWithMessage is request for the routes whose error bodies are
// sentences for the person who clicked (403, 409, 429, 502, 503): 401
// still means the session is gone, and a 403 stays a ForbiddenError so a
// caller can tell it apart, but its text is the toast.
async function requestWithMessage<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init);
  if (res.status === 401) throw new AuthRequiredError();
  if (!res.ok) {
    const text = (await res.text()).trim();
    if (res.status === 403) throw new ForbiddenError(text || "Permission denied.");
    throw new Error(text || `${path.split("?")[0]}: HTTP ${res.status}`);
  }
  if (res.status === 204) return undefined as T;
  const body = await res.text();
  return (body ? JSON.parse(body) : undefined) as T;
}

// fromMock turns a demo answer's status error into the error the live
// response would raise.
function fromMock(e: unknown): never {
  if (e instanceof MockStatusError && e.status === 403) throw new ForbiddenError(e.message);
  throw e;
}

// demoDelay simulates a round-trip so busy states are visible.
function demoDelay(ms = 350): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// ---- demo state ----------------------------------------------------------

let demoData: OfflineDataset | null = null;

function demoDataset(): OfflineDataset {
  if (!demoData) demoData = mockDataset();
  return demoData;
}

// demoPostAction applies the verb's legal transition to the mock dataset,
// mirroring what the controllers would do.
function demoPostAction(name: string, verb: ActionVerb, persona: Persona): void {
  const data = demoDataset();
  const f = data.findings.find((x) => x.name === name);
  if (!f) throw new Error(`finding ${name} not found`);
  if (!mockUserActions(f, persona).includes(verb)) {
    throw new ForbiddenError(
      `Permission denied. User "${persona.label}" may not ${verb} findings in this namespace.`,
    );
  }
  if (!availableActions(f).includes(verb)) {
    throw new ForbiddenError(`Action ${verb} is not available for this finding.`);
  }
  const now = new Date().toISOString();
  if (verb === "approve") {
    f.phase = "Queued";
    f.phaseTimes = [...(f.phaseTimes ?? []), { phase: "Queued", at: now }];
    f.approval = { by: persona.label, at: now, note: "Approved from the status page (demo)." };
    if (f.investigation) f.investigation.awaitApproval = false;
    f.completedAt = undefined;
  } else if (verb === "retry") {
    const target = retryTarget(f) ?? "Enhanced";
    f.phase = target;
    f.phaseTimes = [...(f.phaseTimes ?? []), { phase: target, at: now }];
    f.retry = { by: persona.label, at: now };
    f.completedAt = undefined;
  } else if (verb === "expedite") {
    f.expedite = { by: persona.label, at: now };
  } else {
    f.suspend = verb === "suspend";
  }
}

// ---- public surface ------------------------------------------------------

// offlineDataset is the full local dataset the offline modes answer from.
function offlineDataset(): OfflineDataset {
  return dataMode() === "snapshot" ? window.__PATCHY_STATUS_SNAPSHOT__!.dataset : demoDataset();
}

// offlineRollups is the full rollup list the offline modes carry; the live
// server never sends it whole.
function offlineRollups(): Rollup[] {
  return offlineDataset().rollups ?? [];
}

// offlineHeader is the namespace, version and user the top bar renders.
function offlineHeader(): DatasetHeader {
  const { generatedAt, namespace, version, user } = offlineDataset();
  return { generatedAt, namespace, version, user };
}

// fetchFindings is one page of the findings list, filtered and sorted
// server-side; pass a page's nextCursor to continue it. The first page (no
// cursor) carries the facets. Like the server's, it carries no rollups: the
// metrics page loads its own summary. Offline modes answer the same query
// from their full local list.
export async function fetchFindings(
  q: FindingsQuery,
  persona: Persona = DEFAULT_PERSONA,
  signal?: AbortSignal,
): Promise<FindingsPage> {
  const mode = dataMode();
  if (mode !== "live") {
    const page = queryFindingsLocal(offlineDataset().findings, q);
    const findings =
      mode === "demo" ? page.findings.map((f: Finding) => ({ ...f, userActions: mockUserActions(f, persona) })) : page.findings;
    return { ...offlineHeader(), ...page, findings };
  }
  return request<FindingsPage>(`/api/findings?${queryParams(q)}`, { signal });
}

// suggestFindings is the findings filter's type-ahead for q.q: how many
// findings the other filters match with it as the text search, and the
// repositories whose name contains it.
export async function suggestFindings(q: FindingsQuery, signal?: AbortSignal): Promise<FindingSuggestions> {
  if (dataMode() !== "live") return suggestFindingsLocal(offlineDataset().findings, q);
  return request<FindingSuggestions>(`/api/findings/suggest?${queryParams(q)}`, { signal });
}

// fetchFinding loads one finding's full detail: description, alerts,
// enrichments, the phase log, and the run reports the list payload omits.
// Resolves null when the finding is gone (completed findings expire on a
// TTL); offline modes answer from their full local dataset.
export async function fetchFinding(
  name: string,
  persona: Persona = DEFAULT_PERSONA,
): Promise<Finding | null> {
  const mode = dataMode();
  if (mode !== "live") {
    const f = offlineDataset().findings.find((x) => x.name === name);
    if (!f) return null;
    return mode === "demo" ? { ...f, userActions: mockUserActions(f, persona) } : f;
  }
  const res = await fetch(`/api/findings/${encodeURIComponent(name)}`);
  if (res.status === 404) return null;
  if (res.status === 401) throw new AuthRequiredError();
  if (res.status === 403) {
    const text = (await res.text()).trim();
    throw new ForbiddenError(text || "Permission denied.");
  }
  if (!res.ok) throw new Error(`/api/findings/${name}: HTTP ${res.status}`);
  return (await res.json()) as Finding;
}

// fetchRollups is the metrics summary: the total, model and harness shards,
// the top repositories, and how many repositories exist. The server gates it
// on its own grant (get on findingrollups). It is also the fallback dataset
// (namespace, version, user) when a signed-in user may not view findings.
export async function fetchRollups(): Promise<Dataset> {
  const mode = dataMode();
  if (mode !== "live") {
    return { ...offlineHeader(), user: undefined, ...summarizeRollups(offlineRollups()) };
  }
  return request<Dataset>("/api/rollups");
}

// queryRepositories pages, searches and aggregates repository shards
// server-side (GET /api/rollups/repositories); the offline modes answer the
// same query from their full local list.
export async function queryRepositories(q: RepositoryQuery, signal?: AbortSignal): Promise<RepositoryPage> {
  if (dataMode() !== "live") return queryRepositoriesLocal(offlineRollups(), q);
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v === undefined || v === "" || v === false) continue;
    params.set(k, v === true ? "1" : String(v));
  }
  return request<RepositoryPage>(`/api/rollups/repositories?${params}`, { signal });
}

export async function postAction(
  name: string,
  verb: ActionVerb,
  persona: Persona = DEFAULT_PERSONA,
): Promise<void> {
  const mode = dataMode();
  if (mode === "snapshot") throw new ForbiddenError("Snapshot is read-only.");
  if (mode === "demo") {
    // Simulate a round-trip so busy states are visible.
    await new Promise((resolve) => setTimeout(resolve, 350));
    demoPostAction(name, verb, persona);
    return;
  }
  await request<unknown>(`/api/findings/${encodeURIComponent(name)}/actions/${verb}`, {
    method: "POST",
  });
}

// postAdmin runs one namespace-wide action from the user menu. In demo mode
// reset empties the mock dataset and replay is a no-op, which is enough to
// show the buttons working.
export async function postAdmin(verb: AdminVerb): Promise<void> {
  const mode = dataMode();
  if (mode === "snapshot") throw new ForbiddenError("Snapshot is read-only.");
  if (mode === "demo") {
    await new Promise((resolve) => setTimeout(resolve, 350));
    if (verb === "reset") {
      const data = demoDataset();
      data.findings = [];
      data.rollups = [];
    }
    return;
  }
  await request<unknown>(`/api/admin/${verb}`, { method: "POST" });
}

// ---- configuration view --------------------------------------------------

let demoConfig: ConfigDataset | null = null;

function demoConfigDataset(): ConfigDataset {
  if (!demoConfig) demoConfig = mockConfig();
  return demoConfig;
}

// fetchConfig loads the configuration dataset (Forges, Integrations,
// enhancers). Live mode requires get on integrations server-side; demo and
// snapshot modes answer from the mock kit.
export async function fetchConfig(): Promise<ConfigDataset> {
  if (dataMode() !== "live") return demoConfigDataset();
  return request<ConfigDataset>("/api/config");
}

// postBackfill requests a list-alerts backfill on one integration; the
// optional repositories entries are "owner/" prefixes or exact "owner/name".
// The server answers with the BackfillRequest it created ({request}).
export async function postBackfill(
  name: string,
  repositories: string[],
  persona: Persona = DEFAULT_PERSONA,
): Promise<void> {
  const mode = dataMode();
  if (mode === "snapshot") throw new ForbiddenError("Snapshot is read-only.");
  if (mode === "demo") {
    await new Promise((resolve) => setTimeout(resolve, 350));
    if (!persona.integrationActions.includes("backfill")) {
      throw new ForbiddenError(
        `Permission denied. User "${persona.label}" may not backfill integrations in this namespace.`,
      );
    }
    const integ = demoConfigDataset().integrations.find((i) => i.name === name);
    if (!integ) throw new Error(`integration ${name} not found`);
    const now = new Date().toISOString();
    const request = `backfill-demo-${integ.name}-${Date.now().toString(36)}`;
    integ.backfill = { request, phase: "Pending", requestedBy: persona.label, requestedAt: now, pending: true };
    // The demo "controller" consumes the request shortly after.
    setTimeout(() => {
      integ.backfill = {
        request,
        phase: "Complete",
        lastRunAt: new Date().toISOString(),
        listed: 37,
        ingested: 37,
        requestedBy: persona.label,
        requestedAt: now,
      };
    }, 1500);
    return;
  }
  await request<unknown>(`/api/integrations/${encodeURIComponent(name)}/actions/backfill`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(repositories.length ? { repositories } : {}),
  });
}

// ---- repositories page ---------------------------------------------------

function repoParams(names: string[]): string {
  const params = new URLSearchParams();
  for (const n of names) params.append("repo", n);
  return params.toString();
}

// fetchRepositories loads the tracked repositories' finding counts, rollups
// and backfill state, plus the caller's backfill grants and GitHub links
// (GET /api/repositories, at most 50 names).
export async function fetchRepositories(
  names: string[],
  persona: Persona = DEFAULT_PERSONA,
  signal?: AbortSignal,
): Promise<RepositoriesPayload> {
  const mode = dataMode();
  if (mode !== "live") {
    const data = offlineDataset();
    const out = mockRepositories(data.findings, data.rollups ?? [], names, mode === "demo" ? persona : null);
    return mode === "snapshot" ? { ...out, accessCheck: false, github: undefined } : out;
  }
  return request<RepositoriesPayload>(`/api/repositories?${repoParams(names)}`, { signal });
}

// fetchRepositoryAccess asks whether patchy can read each never-seen
// repository's alerts (GET /api/repositories/access, at most 20 names).
// Repositories patchy has seen come back skipped.
export async function fetchRepositoryAccess(names: string[], signal?: AbortSignal): Promise<RepositoryAccessPayload> {
  const mode = dataMode();
  if (mode === "snapshot") return { available: false, repositories: [] };
  if (mode === "demo") {
    await demoDelay(250);
    const data = offlineDataset();
    return mockRepositoryAccess(data.findings, data.rollups ?? [], names);
  }
  return request<RepositoryAccessPayload>(`/api/repositories/access?${repoParams(names)}`, { signal });
}

// postRepositoryBackfill backfills one repository: at once on the caller's
// grant, or, with request, as a request an operator decides. The server
// picks the integration that can read its alerts. A 403 (no grant, a
// maintainer role too low, GitHub not connected) is a ForbiddenError and a
// 429 (the cooldown, or too many open requests) an Error, both carrying
// the server's sentence.
export async function postRepositoryBackfill(
  repository: string,
  request: boolean,
  persona: Persona = DEFAULT_PERSONA,
): Promise<RepositoryBackfillResult> {
  const mode = dataMode();
  if (mode === "snapshot") throw new ForbiddenError("Snapshot is read-only.");
  if (mode === "demo") {
    await demoDelay();
    const data = offlineDataset();
    try {
      return mockRepositoryBackfill(data.findings, data.rollups ?? [], repository, request, persona);
    } catch (e) {
      fromMock(e);
    }
  }
  return requestWithMessage<RepositoryBackfillResult>("/api/repositories/backfill", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(request ? { repository, request: true } : { repository }),
  });
}

// ---- backfill requests ---------------------------------------------------

// fetchBackfillRequests lists every BackfillRequest, newest first, and
// whether the caller may decide them (GET /api/backfill-requests).
export async function fetchBackfillRequests(
  persona: Persona = DEFAULT_PERSONA,
  signal?: AbortSignal,
): Promise<BackfillRequestsPayload> {
  const mode = dataMode();
  if (mode === "snapshot") return { generatedAt: new Date().toISOString(), requests: [], canDecide: false };
  if (mode === "demo") return mockBackfillRequests(persona.integrationActions.includes("backfill"));
  return request<BackfillRequestsPayload>("/api/backfill-requests", { signal });
}

// postBackfillDecision approves or denies a requested backfill, with an
// optional reason (at most 500 characters).
export async function postBackfillDecision(
  name: string,
  verdict: "approve" | "deny",
  reason = "",
  persona: Persona = DEFAULT_PERSONA,
): Promise<void> {
  const mode = dataMode();
  if (mode === "snapshot") throw new ForbiddenError("Snapshot is read-only.");
  if (mode === "demo") {
    await demoDelay();
    if (!persona.integrationActions.includes("backfill")) {
      throw new ForbiddenError(
        `Permission denied. User "${persona.label}" may not decide backfill requests in this namespace.`,
      );
    }
    try {
      mockBackfillDecision(name, verdict, reason, persona.label);
    } catch (e) {
      fromMock(e);
    }
    return;
  }
  await requestWithMessage<unknown>(`/api/backfill-requests/${encodeURIComponent(name)}/${verdict}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(reason ? { reason } : {}),
  });
}

// ---- GitHub link -----------------------------------------------------------

// fetchGitHubRepositories lists the repositories the caller administers or
// maintains through one Integration's App. Every failure is an Error
// carrying the server's sentence: not connected (409), the connection
// expired (403, "Reconnect GitHub"), or linking off (404). None of them
// means the page is off limits, so none is a ForbiddenError.
export async function fetchGitHubRepositories(
  integration: string,
  signal?: AbortSignal,
): Promise<GitHubRepositoriesPayload> {
  const mode = dataMode();
  if (mode === "snapshot") throw new Error("Snapshot is read-only.");
  if (mode === "demo") {
    await demoDelay(300);
    return mockGitHubRepositories(integration);
  }
  try {
    return await requestWithMessage<GitHubRepositoriesPayload>(
      `/api/github/repositories?integration=${encodeURIComponent(integration)}`,
      { signal },
    );
  } catch (e) {
    if (e instanceof ForbiddenError) throw new Error(e.message);
    throw e;
  }
}

// connectGitHub starts linking a GitHub account through one Integration:
// a full-page navigation through GitHub that returns to back (a path and
// hash). Demo mode connects on the spot.
export async function connectGitHub(integration: string, back: string): Promise<void> {
  const mode = dataMode();
  if (mode === "snapshot") throw new ForbiddenError("Snapshot is read-only.");
  if (mode === "demo") {
    await demoDelay();
    mockGitHubLink(integration, true);
    return;
  }
  location.href = githubConnectHref(integration, back);
}

// disconnectGitHub drops the link to one Integration's GitHub account.
export async function disconnectGitHub(integration: string): Promise<void> {
  const mode = dataMode();
  if (mode === "snapshot") throw new ForbiddenError("Snapshot is read-only.");
  if (mode === "demo") {
    await demoDelay();
    mockGitHubLink(integration, false);
    return;
  }
  await requestWithMessage<void>(`/oauth2/github/${encodeURIComponent(integration)}/disconnect`, { method: "POST" });
}

// subscribeConfig opens the SSE stream for config-changed signals. It is a
// separate subscription so the refetch only happens while the
// configuration view is mounted.
export function subscribeConfig(onChange: () => void): () => void {
  if (dataMode() !== "live") return () => {};
  const es = new EventSource("/events");
  es.addEventListener("config-changed", onChange);
  return () => es.close();
}

// subscribe opens the SSE stream and calls onChange whenever findings change
// server-side. Returns an unsubscribe function. Best-effort: EventSource
// retries on its own, and a failure just means no live refresh. Snapshot and
// demo modes have nothing to subscribe to.
export function subscribe(onChange: () => void): () => void {
  if (dataMode() !== "live") return () => {};
  const es = new EventSource("/events");
  es.addEventListener("findings-changed", onChange);
  return () => es.close();
}

// TranscriptHandlers receive one agent conversation as it arrives.
export interface TranscriptHandlers {
  // onTurn receives each turn once, in order, even across reconnects.
  onTurn: (turn: TranscriptTurn) => void;
  // onEnd fires when the conversation is complete: a finished run's stored
  // record, or a live run whose agent exited.
  onEnd: () => void;
  onError: (err: Error) => void;
  // onReconnecting reports the connection dropped and is being resumed
  // (true), or that turns are flowing again (false).
  onReconnecting?: (reconnecting: boolean) => void;
}

// fetchArchiveURL presigns one archived object of one run. The URL reads the
// object straight from the archive bucket (which must allow GET from this
// page's origin via CORS) and expires after a few minutes, so fetch it right
// before use rather than caching it.
export async function fetchArchiveURL(
  finding: string,
  kind: "investigation" | "remediation",
  attempt: number,
  object: ArchiveObject,
): Promise<PresignedURL> {
  if (dataMode() !== "live") return mockArchiveURL(finding, kind, object);
  return request<PresignedURL>(
    `/api/findings/${encodeURIComponent(finding)}/runs/${kind}/${attempt}/archive/${object}`,
  );
}

// toTranscriptTurn maps one archived transcript line onto the wire turn. The
// archive stores internal/transcript.Turn, which is internal/web.Turn plus a
// format version, so this is the one place the two shapes meet.
export function toTranscriptTurn(raw: unknown): TranscriptTurn | null {
  if (!raw || typeof raw !== "object") return null;
  const { v: _version, ...turn } = raw as Record<string, unknown>;
  if (typeof turn.seq !== "number" || typeof turn.kind !== "string" || typeof turn.role !== "string") {
    return null;
  }
  return turn as unknown as TranscriptTurn;
}

// parseTranscriptJSONL decodes an archived transcript's lines. A malformed
// line costs that line, not the conversation.
export function parseTranscriptJSONL(text: string): TranscriptTurn[] {
  const turns: TranscriptTurn[] = [];
  for (const line of text.split("\n")) {
    if (!line.trim()) continue;
    try {
      const turn = toTranscriptTurn(JSON.parse(line));
      if (turn) turns.push(turn);
    } catch {
      // skip
    }
  }
  return turns;
}

// loadArchivedTranscript reads a run's full conversation from the archive:
// presign, fetch the gzipped JSONL directly from the bucket, and gunzip it in
// the browser. The object is stored as application/gzip with no
// Content-Encoding, so the browser hands over the compressed bytes untouched.
export async function loadArchivedTranscript(
  finding: string,
  kind: "investigation" | "remediation",
  attempt: number,
): Promise<TranscriptTurn[]> {
  const { url } = await fetchArchiveURL(finding, kind, attempt, "transcript");
  const res = await fetch(url, { credentials: "omit" });
  if (!res.ok || !res.body) throw new Error(`archived transcript: HTTP ${res.status}`);
  const text = await new Response(res.body.pipeThrough(new DecompressionStream("gzip"))).text();
  return parseTranscriptJSONL(text);
}

// TRANSCRIPT_REOPENS bounds how many times a terminally failed stream is
// reopened before the error is reported; any delivered turn resets it.
const TRANSCRIPT_REOPENS = 5;

// streamTranscript opens one run's conversation and returns a close function.
//
// The server answers with SSE whether the run is finished or still going, so
// there is a single client path: turns arrive in order and an `end` event says
// the conversation is over. EventSource would otherwise reconnect forever
// after a completed stream, so `end` also closes the connection.
//
// Every turn carries its seq as the SSE id. A dropped connection (or the
// server evicting a viewer that fell behind the live run) is resumed rather
// than replayed: EventSource's own reconnect sends Last-Event-ID, and a stream
// that fails outright is reopened with ?after=<last seq>. Turns at or below the
// last seq seen are dropped either way, so the conversation never repeats.
//
// An archived run's stream answers `archived` instead of turns: the client
// then plays the full record back from the archive (loadArchivedTranscript)
// through the same handlers, so callers never see the difference. archived
// only matters in demo and snapshot modes, which have no server to say so.
export function streamTranscript(
  finding: string,
  kind: "investigation" | "remediation",
  attempt: number,
  handlers: TranscriptHandlers,
  archived = false,
): () => void {
  let stopped = false;
  const playArchived = (after: number) => {
    loadArchivedTranscript(finding, kind, attempt).then(
      (turns) => {
        if (stopped) return;
        turns.filter((t) => t.seq > after).forEach(handlers.onTurn);
        handlers.onEnd();
      },
      (err: unknown) => {
        if (!stopped) handlers.onError(err instanceof Error ? err : new Error(String(err)));
      },
    );
  };

  if (dataMode() !== "live" && archived) {
    playArchived(0);
    return () => {
      stopped = true;
    };
  }
  if (dataMode() !== "live") {
    const turns = mockTranscript(finding, kind);
    // Deliver asynchronously so callers see the same ordering as live mode.
    const timer = setTimeout(() => {
      turns.forEach(handlers.onTurn);
      handlers.onEnd();
    }, 120);
    return () => clearTimeout(timer);
  }

  const path = `/api/findings/${encodeURIComponent(finding)}/runs/${kind}/${attempt}/transcript`;
  let es: EventSource | undefined;
  let last = 0;
  let reopens = 0;
  let reconnecting = false;
  let closed = false;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const setReconnecting = (value: boolean) => {
    if (reconnecting !== value) {
      reconnecting = value;
      handlers.onReconnecting?.(value);
    }
  };
  const close = () => {
    if (!closed) {
      closed = true;
      clearTimeout(timer);
      es?.close();
    }
  };

  const open = () => {
    const source = new EventSource(last > 0 ? `${path}?after=${last}` : path);
    es = source;
    source.addEventListener("turn", (event) => {
      let turn: TranscriptTurn;
      try {
        turn = JSON.parse((event as MessageEvent).data) as TranscriptTurn;
      } catch {
        return; // a malformed turn costs one line, not the stream
      }
      if (turn.seq <= last) return;
      last = turn.seq;
      reopens = 0;
      setReconnecting(false);
      handlers.onTurn(turn);
    });
    source.addEventListener("archived", () => {
      close();
      setReconnecting(false);
      playArchived(last);
    });
    source.addEventListener("end", () => {
      close();
      setReconnecting(false);
      handlers.onEnd();
    });
    source.addEventListener("error", () => {
      if (closed) return;
      if (source.readyState !== EventSource.CLOSED) {
        // EventSource is retrying on its own, with Last-Event-ID.
        setReconnecting(true);
        return;
      }
      source.close();
      if (reopens >= TRANSCRIPT_REOPENS) {
        close();
        setReconnecting(false);
        handlers.onError(new Error("transcript stream closed"));
        return;
      }
      reopens++;
      setReconnecting(true);
      timer = setTimeout(open, 1000 * reopens);
    });
  };
  open();
  return () => {
    stopped = true;
    close();
  };
}
