// Theme switch — a controlled sun/moon pill plus the body-class mechanism
// shared with the other patchy surfaces: dark-default `theme-patchy` paired
// with `theme-patchy-light`, persisted under the same localStorage key so
// the visitor's choice follows them across pages. A pre-paint script in
// index.html restores the class before first render; this hook seeds from
// the DOM and flips both classes on toggle.

import { useState } from "preact/hooks";
import { Icon } from "./icons";

const BASE = "theme-patchy";
const LIGHT = "theme-patchy-light";
const STORAGE_KEY = "patchy-theme";

export type ThemeMode = "dark" | "light";

export function useBodyThemeMode(): [ThemeMode, () => void] {
  const read = (): ThemeMode => (document.body.classList.contains(LIGHT) ? "light" : "dark");
  const [mode, setMode] = useState<ThemeMode>(read);
  const toggle = () => {
    const next: ThemeMode = mode === "dark" ? "light" : "dark";
    document.body.classList.toggle(LIGHT, next === "light");
    document.body.classList.toggle(BASE, next === "dark");
    try {
      localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // Private-mode storage failures only cost persistence.
    }
    setMode(next);
  };
  return [mode, toggle];
}

// Shows the glyph for the destination scheme (sun while dark → go light).
export function ThemeToggle({ mode, onToggle }: { mode: ThemeMode; onToggle: () => void }) {
  const dark = mode === "dark";
  const goingTo = dark ? "light" : "dark";
  return (
    <button
      type="button"
      class="inline-flex cursor-pointer items-center justify-center gap-1.5 rounded-lg border border-line-2 bg-surface px-2.5 py-1.5 font-mono text-[11.5px] text-fg transition-colors"
      onClick={onToggle}
      aria-label={`Switch to ${goingTo} theme`}
      aria-pressed={mode === "light"}
      title={`Switch to ${goingTo} theme`}
    >
      <Icon name={dark ? "sun" : "moon"} size={14} />
      {dark ? "Light" : "Dark"}
    </button>
  );
}
