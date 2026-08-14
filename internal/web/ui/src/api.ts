// Data access: embedded snapshot (offline), demo (committed kit build), or
// the live read-only API + SSE refetch signal.
//
// Authorization is entirely server-side: the payload arrives with each
// finding's permitted verbs already resolved for the requesting user, and a
// mutation is re-checked on POST. The client only reflects 401/403.

import type {
  ActionVerb,
  AdminVerb,
  ConfigDataset,
  Dataset,
  Finding,
  OfflineDataset,
  TranscriptTurn,
} from "./types";
import { availableActions, retryTarget } from "./actions";
import { mockConfig } from "./mock/config";
import { mockDataset, mockTranscript } from "./mock/findings";
import { DEFAULT_PERSONA, type Persona } from "./mock/personas";

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

// ---- demo state ----------------------------------------------------------

let demoData: OfflineDataset | null = null;

function demoDataset(): OfflineDataset {
  if (!demoData) demoData = mockDataset();
  return demoData;
}

// demoPostAction applies the verb's legal transition to the mock dataset,
// mirroring what the controllers would do.
function demoPostAction(name: string, verb: ActionVerb, persona: Persona): void {
  if (!persona.grants.includes(verb)) {
    throw new ForbiddenError(
      `Permission denied. User "${persona.label}" may not ${verb} findings in this namespace.`,
    );
  }
  const data = demoDataset();
  const f = data.findings.find((x) => x.name === name);
  if (!f) throw new Error(`finding ${name} not found`);
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

export async function fetchFindings(persona: Persona = DEFAULT_PERSONA): Promise<Dataset> {
  const mode = dataMode();
  if (mode === "snapshot") return window.__PATCHY_STATUS_SNAPSHOT__!.dataset;
  if (mode === "demo") {
    const data = demoDataset();
    return {
      ...data,
      findings: data.findings.map((f: Finding) => ({ ...f, userActions: [...persona.grants] })),
    };
  }
  return request<Dataset>("/api/findings");
}

// fetchFinding loads one finding's full detail — description, alerts,
// enrichments, the phase log, and the run reports the list payload omits.
// Resolves null when the finding is gone (completed findings expire on a
// TTL); offline modes answer from their full local dataset.
export async function fetchFinding(
  name: string,
  persona: Persona = DEFAULT_PERSONA,
): Promise<Finding | null> {
  const mode = dataMode();
  if (mode !== "live") {
    const data =
      mode === "snapshot" ? window.__PATCHY_STATUS_SNAPSHOT__!.dataset : demoDataset();
    const f = data.findings.find((x) => x.name === name);
    if (!f) return null;
    return mode === "demo" ? { ...f, userActions: [...persona.grants] } : f;
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

// fetchRollups is the always-public statistics projection: the same dataset
// shape with findings empty. Used as the fallback when the findings surface
// is behind authentication the user has not (or cannot) satisfy.
export async function fetchRollups(): Promise<Dataset> {
  const mode = dataMode();
  if (mode !== "live") return fetchFindings();
  return request<Dataset>("/api/rollups");
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
// reset empties the mock dataset and replay is a no-op — enough to show the
// buttons working.
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
    integ.backfill = {
      ...(integ.backfill ?? {}),
      requestedBy: persona.label,
      requestedAt: now,
      pending: true,
    };
    // The demo "controller" consumes the request shortly after.
    setTimeout(() => {
      integ.backfill = {
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

// subscribeConfig opens the SSE stream for config-changed signals — a
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
  onTurn: (turn: TranscriptTurn) => void;
  // onEnd fires when the conversation is complete — a finished run's stored
  // record, or a live run whose agent exited.
  onEnd: () => void;
  onError: (err: Error) => void;
}

// streamTranscript opens one run's conversation and returns a close function.
//
// The server answers with SSE whether the run is finished or still going, so
// there is a single client path: turns arrive in order and an `end` event says
// the conversation is over. EventSource would otherwise reconnect forever
// after a completed stream, so `end` also closes the connection.
export function streamTranscript(
  finding: string,
  kind: "investigation" | "remediation",
  attempt: number,
  handlers: TranscriptHandlers,
): () => void {
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
  const es = new EventSource(path);
  let closed = false;
  const close = () => {
    if (!closed) {
      closed = true;
      es.close();
    }
  };

  es.addEventListener("turn", (event) => {
    try {
      handlers.onTurn(JSON.parse((event as MessageEvent).data) as TranscriptTurn);
    } catch {
      // A malformed turn costs one line, not the stream.
    }
  });
  es.addEventListener("end", () => {
    close();
    handlers.onEnd();
  });
  es.addEventListener("error", () => {
    // EventSource reports transport errors and retries; only a closed
    // connection is terminal for us.
    if (es.readyState === EventSource.CLOSED) {
      close();
      handlers.onError(new Error("transcript stream closed"));
    }
  });
  return close;
}
