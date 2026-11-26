// Demo personas: each previews one authorization level. In live mode the
// server stamps userActions per finding from the requesting user's actual
// grants; personas exist only so the demo can show every gating state.

import type { ActionVerb, IntegrationVerb, MaintainedVerb } from "../types";

export interface Persona {
  id: string;
  label: string;
  grants: ActionVerb[];
  // Whether this persona sees the configuration view (native get on
  // integrations in live mode).
  configView: boolean;
  // Integration-scoped verbs (the configuration view's triggers).
  integrationActions: IntegrationVerb[];
  // Scoped maintainer verbs: the base verb on repositories the persona's
  // connected GitHub account administers or maintains.
  maintainedActions: MaintainedVerb[];
}

export const PERSONAS: Persona[] = [
  { id: "viewer", label: "viewer", grants: [], configView: false, integrationActions: [], maintainedActions: [] },
  {
    id: "maintainer",
    label: "maintainer",
    grants: [],
    configView: false,
    integrationActions: [],
    maintainedActions: ["backfill-maintained", "approve-maintained", "expedite-maintained"],
  },
  { id: "approver", label: "approver", grants: ["approve"], configView: false, integrationActions: [], maintainedActions: [] },
  {
    id: "operator",
    label: "operator",
    grants: ["approve", "retry", "expedite", "suspend", "resume"],
    configView: true,
    integrationActions: ["backfill", "replay", "reset"],
    maintainedActions: [],
  },
];

export const DEFAULT_PERSONA = PERSONAS.find((p) => p.id === "operator")!;
