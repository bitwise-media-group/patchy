import type { Dataset } from "../types";
import type { DataMode } from "../api";
import { signOut } from "../auth";
import { PERSONAS, type Persona } from "../mock/personas";
import { hrefForList, hrefForRollups, type Route } from "../router";
import { PatchMark } from "./PatchMark";
import { ThemeToggle, type ThemeMode } from "./ThemeToggle";

function PersonaSwitcher({ persona, onChange }: { persona: Persona; onChange: (p: Persona) => void }) {
  return (
    <div
      class="flex items-center gap-1 rounded-[9px] border border-dashed border-line-2 py-[3px] pr-[3px] pl-2.5"
      role="group"
      aria-label="Preview as persona"
    >
      <span class="font-mono text-[10px] text-faint">preview as</span>
      {PERSONAS.map((p) => (
        <button
          key={p.id}
          type="button"
          class={`cursor-pointer rounded-[7px] border-0 px-2 py-1 font-mono text-[10.5px] ${
            persona.id === p.id ? "bg-surface-2 text-ink" : "bg-transparent text-muted"
          }`}
          aria-pressed={persona.id === p.id}
          onClick={() => onChange(p)}
        >
          {p.label}
        </button>
      ))}
    </div>
  );
}

const NAV_LINK = "rounded-[7px] px-2.5 py-1 text-[12.5px] font-semibold no-underline";

export function TopBar({
  dataset,
  mode,
  route,
  themeMode,
  onToggleTheme,
  persona,
  onPersonaChange,
}: {
  dataset: Dataset | null;
  mode: DataMode;
  route: Route;
  themeMode: ThemeMode;
  onToggleTheme: () => void;
  persona: Persona;
  onPersonaChange: (p: Persona) => void;
}) {
  const navClass = (active: boolean) =>
    `${NAV_LINK} ${active ? "bg-surface-2 text-fg" : "text-muted"}`;
  return (
    <header class="sticky top-0 z-30 border-b border-line bg-[color-mix(in_oklab,var(--patchy-bg)_86%,transparent)] backdrop-blur-xl">
      <div class="mx-auto flex min-h-[58px] w-[min(1240px,calc(100%-40px))] flex-wrap items-center gap-4 py-1.5 max-sm:w-[calc(100%-28px)]">
        <a class="inline-flex items-center gap-2 font-mono text-[15px] font-bold tracking-tight text-fg no-underline" href={hrefForList()} aria-label="Findings list">
          <PatchMark />
          <span>
            patchy <em class="font-medium text-muted not-italic">· status</em>
          </span>
        </a>
        <nav class="flex gap-1 rounded-[9px] border border-line bg-surface p-[3px]" aria-label="Views">
          <a href={hrefForList()} class={navClass(route.view !== "rollups")}>
            Findings
          </a>
          <a href={hrefForRollups()} class={navClass(route.view === "rollups")}>
            Rollups
          </a>
        </nav>
        <div class="flex items-center gap-3 font-mono text-[10.5px] text-muted max-sm:hidden">
          {mode === "live" ? (
            <span class="inline-flex items-center gap-1.5 text-ink">
              <span class="ps-live-dot" /> live
            </span>
          ) : (
            <span class="rounded-full border border-line-2 bg-surface px-2 py-[3px] text-[9px] tracking-[0.08em] uppercase">
              {mode}
            </span>
          )}
          {dataset?.namespace ? <span>ns: {dataset.namespace}</span> : null}
          {dataset?.version ? <span>{dataset.version}</span> : null}
        </div>
        <div class="ml-auto flex items-center gap-3">
          {mode === "demo" ? <PersonaSwitcher persona={persona} onChange={onPersonaChange} /> : null}
          {dataset?.user?.loggedIn ? (
            <div class="flex items-center gap-2 font-mono text-[10.5px] text-muted">
              <span class="max-sm:hidden" title="Signed in">
                {dataset.user.name}
              </span>
              <button type="button" class="ps-action" onClick={() => void signOut()}>
                Sign out
              </button>
            </div>
          ) : null}
          <ThemeToggle mode={themeMode} onToggle={onToggleTheme} />
        </div>
      </div>
    </header>
  );
}
