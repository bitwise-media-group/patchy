import { Icon } from "./icons";

export interface ToastItem {
  id: number;
  message: string;
  tone: "red" | "green";
}

const TONE = {
  red: "text-red border-[color-mix(in_oklab,var(--patchy-red)_40%,var(--patchy-line-2))]",
  green: "text-ink border-[color-mix(in_oklab,var(--patchy-green)_40%,var(--patchy-line-2))]",
};

export function Toasts({ toasts, onDismiss }: { toasts: ToastItem[]; onDismiss: (id: number) => void }) {
  if (toasts.length === 0) return null;
  return (
    <div class="fixed right-4.5 bottom-4.5 z-60 flex max-w-[min(420px,calc(100vw-36px))] flex-col gap-2" aria-live="polite">
      {toasts.map((t) => (
        <div
          class={`animate-rise flex items-start gap-2 rounded-[10px] border bg-surface px-3 py-2.5 text-[12.5px] shadow-strong ${TONE[t.tone]}`}
          key={t.id}
        >
          <Icon name={t.tone === "red" ? "alertTriangle" : "check"} size={14} class="mt-px flex-none" />
          <span>{t.message}</span>
          <button type="button" class="ml-auto cursor-pointer border-0 bg-transparent p-0 text-inherit" onClick={() => onDismiss(t.id)} aria-label="Dismiss">
            <Icon name="x" size={12} />
          </button>
        </div>
      ))}
    </div>
  );
}
