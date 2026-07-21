// Data access: embedded snapshot (offline), demo (committed kit build), or
// the live read-only API + SSE refetch signal.
//
// Authorization is entirely server-side: the payload arrives with each
// finding's permitted verbs already resolved for the requesting user, and a
// mutation is re-checked on POST. The client only reflects 401/403.

import type { ActionVerb, Dataset, Finding } from "./types";
import { availableActions } from "./actions";
import { mockDataset } from "./mock/findings";
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
  dataset: Dataset;
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

let demoData: Dataset | null = null;

function demoDataset(): Dataset {
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
