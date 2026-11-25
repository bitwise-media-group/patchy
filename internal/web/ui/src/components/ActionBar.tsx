// User actions on a finding. The bar renders nothing when the server granted
// no verbs (userActions empty/absent), so read-only users never see disabled
// buttons. Each is a ConfirmButton; only one is armed at a time.

import { useEffect, useState } from "preact/hooks";
import type { ActionVerb, Finding } from "../types";
import { visibleActions } from "../actions";
import { type IconName } from "./icons";
import { ConfirmButton } from "./ConfirmButton";

const VERB_META: Record<ActionVerb, { label: string; icon: IconName; primary: boolean }> = {
  approve: { label: "Approve", icon: "check", primary: true },
  retry: { label: "Retry", icon: "rotateCcw", primary: true },
  expedite: { label: "Expedite", icon: "zap", primary: false },
  suspend: { label: "Suspend", icon: "pause", primary: false },
  resume: { label: "Resume", icon: "play", primary: false },
};

export function ActionBar({
  finding,
  busy,
  onAction,
}: {
  finding: Finding;
  busy: ActionVerb | null;
  onAction: (verb: ActionVerb) => void;
}) {
  const [confirming, setConfirming] = useState<ActionVerb | null>(null);
  useEffect(() => setConfirming(null), [finding.name, finding.phase, finding.suspend]);

  if (!finding.userActions?.length) return null;
  const verbs = visibleActions(finding);
  if (verbs.length === 0) return null;

  return (
    <div
      class="sticky bottom-0 mt-6 flex flex-wrap items-center gap-2.5 rounded-[10px] border border-line-2 bg-[color-mix(in_oklab,var(--patchy-surface)_94%,transparent)] px-4 py-3 shadow-card backdrop-blur-md"
      role="group"
      aria-label="Actions"
    >
      <span class="text-[13px] text-muted">Actions available to you</span>
      {verbs.map((verb) => {
        const meta = VERB_META[verb];
        const label =
          verb === "approve" && finding.phase === "HandedOff" ? "Approve & revive" : meta.label;
        return (
          <ConfirmButton
            key={verb}
            label={label}
            icon={meta.icon}
            primary={meta.primary}
            busy={busy === verb}
            disabled={busy !== null && busy !== verb}
            armed={confirming === verb}
            onArmedChange={(armed) => setConfirming(armed ? verb : null)}
            onConfirm={() => onAction(verb)}
          />
        );
      })}
    </div>
  );
}
