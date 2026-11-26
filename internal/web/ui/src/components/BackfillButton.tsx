// The backfill trigger on the configuration page and the repositories page:
// a ConfirmButton that also reports a request already waiting for the
// controller.

import { ConfirmButton } from "./ConfirmButton";

export function BackfillButton({
  label = "Backfill",
  confirmLabel = "Confirm backfill",
  busy = false,
  pending = false,
  disabled = false,
  resetKey,
  onConfirm,
}: {
  label?: string;
  // What the second click will do, e.g. "Confirm: backfill everything".
  confirmLabel?: string;
  busy?: boolean;
  // A request is already waiting for the controller.
  pending?: boolean;
  // Not allowed yet (the repositories page's cooldown).
  disabled?: boolean;
  resetKey?: string;
  onConfirm: () => void;
}) {
  return (
    <ConfirmButton
      icon="rotateCcw"
      label={pending ? "Backfill pending" : label}
      confirmLabel={confirmLabel}
      busyLabel="Requesting…"
      busy={busy}
      disabled={pending || disabled}
      resetKey={resetKey}
      onConfirm={onConfirm}
    />
  );
}
