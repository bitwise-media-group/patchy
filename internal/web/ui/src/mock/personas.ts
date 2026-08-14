// Demo personas — each previews one authorization level. In live mode the
// server stamps userActions per finding from the requesting user's actual
// grants; personas exist only so the demo can show every gating state.

import type { ActionVerb, Dataset, IntegrationVerb } from "../types";

export interface Persona {
  id: string;
  label: string;
  grants: ActionVerb[];
  // Whether this persona sees the configuration view (native get on
  // integrations in live mode).
  configView: boolean;
  // Integration-scoped verbs (the configuration view's triggers).
  integrationActions: IntegrationVerb[];
}

export const PERSONAS: Persona[] = [
  { id: "viewer", label: "viewer", grants: [], configView: false, integrationActions: [] },
  { id: "approver", label: "approver", grants: ["approve"], configView: false, integrationActions: [] },
  {
    id: "operator",
    label: "operator",
    grants: ["approve", "retry", "expedite", "suspend", "resume"],
    configView: true,
    integrationActions: ["backfill", "replay", "reset"],
  },
];

export const DEFAULT_PERSONA = PERSONAS[2];

// applyPersona stamps the persona's grants onto every finding, the way the
// server would stamp the requesting user's resolved verbs.
export function applyPersona(dataset: Dataset, persona: Persona): Dataset {
  return {
    ...dataset,
    findings: dataset.findings.map((f) => ({ ...f, userActions: [...persona.grants] })),
  };
}
