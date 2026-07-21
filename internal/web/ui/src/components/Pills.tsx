// Status pills. One base Pill with the established patchy tone grammar:
// green = done/progress, seedling = in-flight, amber = needs a human,
// soil = origin/handed-off/dismissed, red = failure/critical.

import type { ComponentChildren } from "preact";
import type { Level, Phase, Rating, Recommendation } from "../types";
import { PHASE_LABELS } from "../format";

export type Tone =
  | "green"
  | "seedling"
  | "amber"
  | "soil"
  | "red"
  | "neutral"
  | "sev-low"
  | "sev-medium"
  | "sev-high"
  | "sev-critical";

export function Pill({
  tone = "green",
  label,
  children,
  title,
}: {
  tone?: Tone;
  label?: string;
  children: ComponentChildren;
  title?: string;
}) {
  return (
    <span class={`ps-pill ps-pill--${tone}`} title={title}>
      {label ? <span class="ps-pill__label">{label}</span> : null}
      {children}
    </span>
  );
}

// Severity gets its own ramp — the state tones (soil/amber) are reserved
// for lifecycle meaning, so `high` must never read like `dismissed`.
const SEVERITY_TONE: Record<Level, Tone> = {
  critical: "sev-critical",
  high: "sev-high",
  medium: "sev-medium",
  low: "sev-low",
};

export function SeverityPill({ severity, label }: { severity?: Level; label?: string }) {
  if (!severity) return <Pill tone="neutral" label={label}>unknown</Pill>;
  return (
    <Pill tone={SEVERITY_TONE[severity]} label={label}>
      {severity}
    </Pill>
  );
}

const PHASE_TONE: Record<Phase, Tone> = {
  Opened: "soil",
  Enhanced: "seedling",
  Investigating: "seedling",
  AwaitingApproval: "amber",
  Queued: "seedling",
  Remediating: "seedling",
  InReview: "seedling",
  Remediated: "green",
  Failed: "red",
  Dismissed: "soil",
  HandedOff: "amber",
};

export function PhasePill({ phase, label }: { phase?: Phase; label?: string }) {
  if (!phase) return <Pill tone="neutral" label={label}>unknown</Pill>;
  return (
    <Pill tone={PHASE_TONE[phase]} label={label}>
      {PHASE_LABELS[phase]}
    </Pill>
  );
}

const RATING_TONE: Record<Rating, Tone> = {
  none: "sev-low",
  low: "sev-low",
  medium: "sev-medium",
  high: "sev-high",
  critical: "sev-critical",
};

export function RatingPill({ rating }: { rating?: Rating }) {
  if (!rating) return <Pill tone="neutral">—</Pill>;
  return <Pill tone={RATING_TONE[rating]}>{rating}</Pill>;
}

const VERDICT_TONE: Record<Recommendation, Tone> = {
  remediate: "green",
  ignore: "soil",
  manual: "amber",
};

export function VerdictPill({ verdict }: { verdict?: Recommendation }) {
  if (!verdict) return <span class="text-faint">—</span>;
  return <Pill tone={VERDICT_TONE[verdict]}>{verdict}</Pill>;
}
