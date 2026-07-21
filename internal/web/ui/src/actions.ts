// Pure action-gating helpers. Authorization comes from the server as
// Finding.userActions; these helpers only intersect that with what the
// finding's state machine allows, so a granted verb still hides when it
// has no legal transition (e.g. approve outside AwaitingApproval/HandedOff).

import type { ActionVerb, Finding } from "./types";
import { TERMINAL_PHASES } from "./types";

// availableActions returns what the state machine allows right now:
//   approve — AwaitingApproval → Queued, or HandedOff → Queued (revival)
//   suspend — pause a non-terminal, not-yet-suspended finding
//   resume  — clear a suspension
export function availableActions(f: Finding): ActionVerb[] {
  const verbs: ActionVerb[] = [];
  if (f.phase === "AwaitingApproval" || f.phase === "HandedOff") verbs.push("approve");
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
