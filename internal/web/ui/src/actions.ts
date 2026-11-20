// Pure action-gating helpers. Authorization comes from the server as
// Finding.userActions; these helpers only intersect that with what the
// finding's state machine allows, so a granted verb still hides when it
// has no legal transition (e.g. approve outside AwaitingApproval/HandedOff).

import type { ActionVerb, Finding, Phase } from "./types";
import { TERMINAL_PHASES } from "./types";

// RETRY_RECOVERS maps the phase a finding failed from onto the phase a retry
// recovers it to (api/v1alpha1 RetryTarget).
const RETRY_RECOVERS: Partial<Record<Phase, Phase>> = {
  Investigating: "Enhanced",
  Remediating: "Queued",
  InReview: "Queued",
};

// EXPEDITABLE_PHASES are the phases with waiting stages still ahead
// (accumulation, minimum age, or a queue) that an expedite skips.
const EXPEDITABLE_PHASES: ReadonlySet<Phase> = new Set([
  "Opened",
  "Enhanced",
  "Investigating",
  "AwaitingApproval",
  "Queued",
]);

// retryTarget returns the phase a Failed finding would recover to, or null
// when it is not Failed / has no retryable history.
export function retryTarget(f: Finding): Phase | null {
  if (f.phase !== "Failed") return null;
  let prior: Phase | undefined;
  for (const pt of f.phaseTimes ?? []) {
    if (pt.phase !== "Failed") prior = pt.phase;
  }
  return (prior && RETRY_RECOVERS[prior]) ?? null;
}

// retryPending reports a recorded retry the controllers have not consumed
// yet (newer than the failure's completedAt; RFC3339 UTC compares
// lexicographically).
function retryPending(f: Finding): boolean {
  if (!f.retry) return false;
  return !f.completedAt || f.retry.at > f.completedAt;
}

// availableActions returns what the state machine allows right now:
//   approve:  AwaitingApproval → Queued, or HandedOff → Queued (revival)
//   retry:    recover a Failed finding to the state it failed from
//   expedite: skip the waiting stages (accumulation, min age, queue)
//   suspend:  pause a non-terminal, not-yet-suspended finding
//   resume:   clear a suspension
export function availableActions(f: Finding): ActionVerb[] {
  const verbs: ActionVerb[] = [];
  if (f.phase === "AwaitingApproval" || f.phase === "HandedOff") verbs.push("approve");
  if (retryTarget(f) !== null && !retryPending(f)) verbs.push("retry");
  if (!f.expedite && f.phase && EXPEDITABLE_PHASES.has(f.phase)) verbs.push("expedite");
  if (f.suspend) {
    verbs.push("resume");
  } else if (f.phase && !TERMINAL_PHASES.has(f.phase)) {
    verbs.push("suspend");
  }
  return verbs;
}

// visibleActions intersects the state machine with the user's grants.
export function visibleActions(f: Finding): ActionVerb[] {
  const granted = f.userActions ?? [];
  if (granted.length === 0) return [];
  return availableActions(f).filter((verb) => granted.includes(verb));
}
