// ConfirmButton is every action that writes something: the first click arms
// it (amber, saying what the second click will do), the second does it.
// Cancel appears to its left, so the armed button stays under the cursor for
// the confirming click. The finding action bar and both backfill triggers
// use it, so arming reads the same everywhere.

import { useEffect, useState } from "preact/hooks";
import { Icon, type IconName } from "./icons";

export function ConfirmButton({
  label,
  confirmLabel = `Confirm ${label.toLowerCase()}`,
  busyLabel = "Working…",
  icon,
  primary = false,
  busy = false,
  disabled = false,
  resetKey,
  armed: controlled,
  onArmedChange,
  onConfirm,
}: {
  label: string;
  confirmLabel?: string;
  busyLabel?: string;
  icon: IconName;
  // A filled button, for the action a view exists for (approve, retry).
  primary?: boolean;
  busy?: boolean;
  disabled?: boolean;
  // Disarms the button when it changes, so a changed request is confirmed
  // anew.
  resetKey?: string;
  // Controlled arming, for a group where only one may be armed at a time.
  armed?: boolean;
  onArmedChange?: (armed: boolean) => void;
  onConfirm: () => void;
}) {
  const [own, setOwn] = useState(false);
  const armed = (controlled ?? own) && !busy && !disabled;
  const setArmed = (next: boolean) => {
    setOwn(next);
    onArmedChange?.(next);
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => setOwn(false), [resetKey]);

  return (
    <span class="inline-flex flex-none items-center gap-1.5">
      {armed ? (
        <button type="button" class="ps-action ps-action--quiet" onClick={() => setArmed(false)}>
          Cancel
        </button>
      ) : null}
      <button
        type="button"
        class={`ps-action ${armed ? "is-confirming" : primary ? "ps-action--primary" : ""}`}
        disabled={busy || disabled}
        onClick={() => {
          if (armed) {
            setArmed(false);
            onConfirm();
          } else {
            setArmed(true);
          }
        }}
      >
        <Icon name={icon} size={14} />
        {busy ? busyLabel : armed ? confirmLabel : label}
      </button>
    </span>
  );
}
