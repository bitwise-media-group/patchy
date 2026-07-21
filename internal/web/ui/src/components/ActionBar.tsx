// User actions on a finding. The bar renders nothing when the server granted
// no verbs (userActions empty/absent) — read-only users never see disabled
// buttons. Each button asks for a second click to confirm.

import { useEffect, useState } from "preact/hooks";
import type { ActionVerb, Finding } from "../types";
import { visibleActions } from "../actions";
import { Icon, type IconName } from "./icons";

const VERB_META: Record<ActionVerb, { label: string; icon: IconName; primary: boolean }> = {
  approve: { label: "Approve", icon: "check", primary: true },
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
      class="sticky bottom-0 mt-6 flex flex-wrap items-center gap-2.5 rounded-xl border border-line-2 bg-[color-mix(in_oklab,var(--patchy-surface)_94%,transparent)] px-4 py-3 shadow-card backdrop-blur-md"
      role="group"
      aria-label="Actions"
    >
      <span class="font-mono text-[10.5px] text-faint">Actions available to you:</span>
      {verbs.map((verb) => {
        const meta = VERB_META[verb];
        const label =
          verb === "approve" && finding.phase === "HandedOff" ? "Approve & revive" : meta.label;
        const isBusy = busy === verb;
        const isConfirming = confirming === verb;
        return (
          <button
            key={verb}
            type="button"
            class={`ps-action ${meta.primary ? "ps-action--primary" : ""} ${isConfirming ? "is-confirming" : ""}`}
            disabled={busy !== null}
            onClick={() => {
              if (isConfirming) {
                setConfirming(null);
                onAction(verb);
              } else {
                setConfirming(verb);
              }
            }}
          >
            <Icon name={meta.icon} size={14} />
            {isBusy ? "Working…" : isConfirming ? `Confirm ${label.toLowerCase()}?` : label}
          </button>
        );
      })}
      {confirming ? (
        <button
          type="button"
          class="cursor-pointer border-0 bg-transparent font-mono text-[11px] text-muted underline"
          onClick={() => setConfirming(null)}
        >
          cancel
        </button>
      ) : null}
    </div>
  );
}
