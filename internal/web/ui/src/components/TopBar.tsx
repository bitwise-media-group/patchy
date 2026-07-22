import { useEffect, useRef, useState } from "preact/hooks";
import type { AdminVerb, Dataset } from "../types";
import type { DataMode } from "../api";
import { readProvider, signOut } from "../auth";
import { PERSONAS, type Persona } from "../mock/personas";
import { hrefForList, hrefForRollups, type Route } from "../router";
import { Icon } from "./icons";
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

const MENU_ITEM =
  "flex w-full cursor-pointer items-center gap-2 rounded-[7px] border-0 bg-transparent px-2.5 py-1.5 text-left font-mono text-[11.5px] text-fg hover:bg-surface-2 disabled:cursor-default disabled:opacity-50";

// UserMenu drops from the signed-in name: the namespace-wide demo tools
// (replay, reset — rendered only when the server granted the verb) and
// sign-out. Reset arms on the first click and deletes on the second.
function UserMenu({
  name,
  adminActions,
  adminBusy,
  onAdmin,
}: {
  name: string;
  adminActions: AdminVerb[];
  adminBusy: AdminVerb | null;
  onAdmin: (verb: AdminVerb) => void;
}) {
  const [open, setOpen] = useState(false);
  const [armedReset, setArmedReset] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) {
        setOpen(false);
        setArmedReset(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        setArmedReset(false);
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const run = (verb: AdminVerb) => {
    setOpen(false);
    setArmedReset(false);
    onAdmin(verb);
  };

  return (
    <div class="relative" ref={ref}>
      <button
        type="button"
        class="inline-flex cursor-pointer items-center gap-1.5 rounded-lg border border-line-2 bg-surface px-2.5 py-1.5 font-mono text-[11.5px] text-fg transition-colors hover:border-turf"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => {
          setOpen((o) => !o);
          setArmedReset(false);
        }}
      >
        <span class="max-w-[180px] truncate">{name}</span>
        <Icon name="chevronDown" size={12} class={`transition-transform ${open ? "rotate-180" : ""}`} />
      </button>
      {open ? (
        <div
          class="absolute right-0 z-40 mt-1.5 flex min-w-[210px] flex-col gap-px rounded-[10px] border border-line bg-surface p-1 shadow-strong"
          role="menu"
        >
          {adminActions.includes("replay") ? (
            <button
              type="button"
              class={MENU_ITEM}
              role="menuitem"
              disabled={adminBusy !== null}
              onClick={() => run("replay")}
            >
              <Icon name="rotateCcw" size={13} class="flex-none text-muted" />
              {adminBusy === "replay" ? "Requesting replay…" : "Replay deliveries"}
            </button>
          ) : null}
          {adminActions.includes("reset") ? (
            <button
              type="button"
              class={`${MENU_ITEM} ${armedReset ? "bg-[color-mix(in_oklab,var(--patchy-red)_12%,transparent)] text-red hover:bg-[color-mix(in_oklab,var(--patchy-red)_18%,transparent)]" : "text-red"}`}
              role="menuitem"
              disabled={adminBusy !== null}
              onClick={() => (armedReset ? run("reset") : setArmedReset(true))}
            >
              <Icon name={armedReset ? "alertTriangle" : "trash"} size={13} class="flex-none" />
              {adminBusy === "reset"
                ? "Resetting…"
                : armedReset
                  ? "Click again to delete everything"
                  : "Reset all data"}
            </button>
          ) : null}
          {adminActions.length > 0 ? <div class="mx-1 my-1 h-px bg-line" role="separator" /> : null}
          <button type="button" class={MENU_ITEM} role="menuitem" onClick={() => void signOut()}>
            <Icon name="logOut" size={13} class="flex-none text-muted" />
            Sign out
          </button>
        </div>
      ) : null}
    </div>
  );
}

export function TopBar({
  dataset,
  mode,
  route,
  themeMode,
  onToggleTheme,
  persona,
  onPersonaChange,
  adminBusy,
  onAdmin,
}: {
  dataset: Dataset | null;
  mode: DataMode;
  route: Route;
  themeMode: ThemeMode;
  onToggleTheme: () => void;
  persona: Persona;
  onPersonaChange: (p: Persona) => void;
  adminBusy: AdminVerb | null;
  onAdmin: (verb: AdminVerb) => void;
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
          {/* The provider cookie keeps the menu reachable when the dataset
              carries no user (e.g. the 403 fallback to public rollups). */}
          {dataset?.user?.loggedIn || (mode === "live" && readProvider()?.authenticated) ? (
            <UserMenu
              name={dataset?.user?.loggedIn ? dataset.user.name : "account"}
              adminActions={dataset?.user?.adminActions ?? []}
              adminBusy={adminBusy}
              onAdmin={onAdmin}
            />
          ) : null}
          <ThemeToggle mode={themeMode} onToggle={onToggleTheme} />
        </div>
      </div>
    </header>
  );
}
