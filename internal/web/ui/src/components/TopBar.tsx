// The top bar. Wide screens: brand, the view links (flat text, the current
// one carrying an accent rule on the header's bottom edge), the data mode,
// then the demo persona switcher, account menu and theme toggle. Below the
// md breakpoint all of that folds into one menu button and a panel that
// opens under the bar, with full-width tap targets.

import { useEffect, useRef, useState } from "preact/hooks";
import type { AdminVerb, DatasetHeader } from "../types";
import type { DataMode } from "../api";
import { readProvider, signOut } from "../auth";
import { PERSONAS, type Persona } from "../mock/personas";
import { hrefForConfig, hrefForList, hrefForMetrics, hrefForRepositories, type Route } from "../router";
import { Icon } from "./icons";
import { PatchMark } from "./PatchMark";
import { ThemeToggle, type ThemeMode } from "./ThemeToggle";

function PersonaSwitcher({ persona, onChange }: { persona: Persona; onChange: (p: Persona) => void }) {
  return (
    <div
      class="flex items-center gap-1 rounded-lg border border-dashed border-line-2 py-[3px] pr-[3px] pl-2.5"
      role="group"
      aria-label="Preview as persona"
    >
      <span class="text-[12.5px] whitespace-nowrap text-muted">Preview as</span>
      {PERSONAS.map((p) => (
        <button
          key={p.id}
          type="button"
          class={`cursor-pointer rounded-md border-0 px-2 py-1 text-[12.5px] ${
            persona.id === p.id ? "bg-surface-2 font-semibold text-fg" : "bg-transparent text-muted hover:text-fg"
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

// Flat text links; the current view carries a 2px accent rule that sits on
// the header's bottom border, so the bar reads as one surface.
const NAV_LINK =
  "relative inline-flex items-center px-3 text-[14px] font-semibold whitespace-nowrap no-underline transition-colors hover:text-fg " +
  "after:absolute after:inset-x-3 after:bottom-0 after:h-0.5 after:rounded-t-full after:content-['']";

const MENU_ITEM =
  "flex w-full cursor-pointer items-center gap-2.5 rounded-md border-0 bg-transparent px-2.5 py-2 text-left text-[13.5px] text-fg hover:bg-surface-2 disabled:cursor-default disabled:opacity-50";

// AccountActions: the namespace-wide demo tools (replay and reset, rendered
// only when the server granted the verb) and sign-out. Reset arms on the
// first click and deletes on the second. Shared by the desktop dropdown
// (as menu items) and the compact menu (as plain buttons).
function AccountActions({
  adminActions,
  adminBusy,
  onAdmin,
  onDone,
  menu,
}: {
  adminActions: AdminVerb[];
  adminBusy: AdminVerb | null;
  onAdmin: (verb: AdminVerb) => void;
  onDone: () => void;
  menu: boolean;
}) {
  const [armedReset, setArmedReset] = useState(false);
  const role = menu ? "menuitem" : undefined;
  const run = (verb: AdminVerb) => {
    setArmedReset(false);
    onDone();
    onAdmin(verb);
  };
  return (
    <>
      {adminActions.includes("replay") ? (
        <button type="button" class={MENU_ITEM} role={role} disabled={adminBusy !== null} onClick={() => run("replay")}>
          <Icon name="rotateCcw" size={15} class="flex-none text-muted" />
          {adminBusy === "replay" ? "Requesting replay…" : "Replay deliveries"}
        </button>
      ) : null}
      {adminActions.includes("reset") ? (
        <button
          type="button"
          class={`${MENU_ITEM} ${armedReset ? "bg-[color-mix(in_oklab,var(--patchy-red)_12%,transparent)] text-red hover:bg-[color-mix(in_oklab,var(--patchy-red)_18%,transparent)]" : "text-red"}`}
          role={role}
          disabled={adminBusy !== null}
          onClick={() => (armedReset ? run("reset") : setArmedReset(true))}
        >
          <Icon name={armedReset ? "alertTriangle" : "trash"} size={15} class="flex-none" />
          {adminBusy === "reset" ? "Resetting…" : armedReset ? "Select again to delete everything" : "Reset all data"}
        </button>
      ) : null}
      {adminActions.length > 0 ? <div class="mx-1 my-1 h-px bg-line" role="separator" /> : null}
      <button type="button" class={MENU_ITEM} role={role} onClick={() => void signOut()}>
        <Icon name="logOut" size={15} class="flex-none text-muted" />
        Sign out
      </button>
    </>
  );
}

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
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  return (
    <div class="relative" ref={ref}>
      <button
        type="button"
        class="inline-flex min-h-9 cursor-pointer items-center gap-1.5 rounded-lg border-0 bg-transparent px-2.5 text-[13.5px] text-fg transition-colors hover:bg-surface-2"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <span class="max-w-[180px] truncate">{name}</span>
        <Icon name="chevronDown" size={12} class={`transition-transform ${open ? "rotate-180" : ""}`} />
      </button>
      {open ? (
        <div class="absolute right-0 z-40 mt-1.5 flex min-w-[220px] flex-col gap-px rounded-[10px] border border-line bg-surface p-1 shadow-strong" role="menu">
          <AccountActions adminActions={adminActions} adminBusy={adminBusy} onAdmin={onAdmin} onDone={() => setOpen(false)} menu />
        </div>
      ) : null}
    </div>
  );
}

function ModeBadge({ mode }: { mode: DataMode }) {
  return mode === "live" ? (
    <span class="inline-flex items-center gap-1.5 text-ink" title="Updates arrive as they happen">
      <span class="ps-live-dot" /> Live
    </span>
  ) : (
    <span class="rounded-full border border-line-2 px-2 py-px">{mode === "demo" ? "Demo data" : "Snapshot"}</span>
  );
}

export function TopBar({
  dataset,
  mode,
  route,
  configView,
  themeMode,
  onToggleTheme,
  persona,
  onPersonaChange,
  adminBusy,
  onAdmin,
}: {
  dataset: DatasetHeader | null;
  mode: DataMode;
  route: Route;
  // Whether the configuration view is reachable for this user; the nav
  // link renders only when true.
  configView: boolean;
  themeMode: ThemeMode;
  onToggleTheme: () => void;
  persona: Persona;
  onPersonaChange: (p: Persona) => void;
  adminBusy: AdminVerb | null;
  onAdmin: (verb: AdminVerb) => void;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  const menuButton = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);

  // Navigating closes the compact menu.
  const routeKey = JSON.stringify(route);
  useEffect(() => setMenuOpen(false), [routeKey]);

  useEffect(() => {
    if (!menuOpen) return;
    panel.current?.querySelector<HTMLElement>("a, button")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setMenuOpen(false);
        menuButton.current?.focus();
      }
    };
    // Grown past the breakpoint with the menu open: the panel is hidden, so
    // drop its state rather than reopening it on the next shrink.
    const wide = window.matchMedia("(min-width: 48rem)");
    const onWide = () => wide.matches && setMenuOpen(false);
    document.addEventListener("keydown", onKey);
    wide.addEventListener("change", onWide);
    return () => {
      document.removeEventListener("keydown", onKey);
      wide.removeEventListener("change", onWide);
    };
  }, [menuOpen]);

  const views: { href: string; label: string; active: boolean }[] = [
    { href: hrefForList(), label: "Findings", active: route.view === "list" || route.view === "detail" },
    { href: hrefForRepositories(), label: "My repositories", active: route.view === "repositories" },
    { href: hrefForMetrics(), label: "Metrics", active: route.view === "metrics" },
    ...(configView ? [{ href: hrefForConfig(), label: "Configuration", active: route.view === "config" }] : []),
  ];

  // The provider cookie keeps the account reachable when there is no
  // dataset user (e.g. findings and metrics both answered 403).
  const signedIn = Boolean(dataset?.user?.loggedIn || (mode === "live" && readProvider()?.authenticated));
  const userName = dataset?.user?.loggedIn ? dataset.user.name : "account";
  const adminActions = dataset?.user?.adminActions ?? [];

  return (
    <header class="sticky top-0 z-30 border-b border-line bg-[color-mix(in_oklab,var(--patchy-bg)_86%,transparent)] backdrop-blur-xl">
      <div class="mx-auto flex min-h-[60px] w-[min(1240px,calc(100%-40px))] items-center gap-x-6 max-sm:w-[calc(100%-32px)]">
        <a class="inline-flex items-center gap-2.5 text-[18px] font-bold tracking-[-0.02em] text-fg no-underline" href={hrefForList()}>
          <PatchMark />
          <span>patchy</span>
          <span class="sr-only">, findings</span>
        </a>
        <nav class="flex min-h-[60px] items-stretch self-stretch max-md:hidden" aria-label="Views">
          {views.map((v) => (
            <a
              key={v.label}
              href={v.href}
              class={`${NAV_LINK} ${v.active ? "text-fg after:bg-turf" : "text-muted after:bg-transparent"}`}
              aria-current={v.active ? "page" : undefined}
            >
              {v.label}
            </a>
          ))}
        </nav>
        <div class="flex items-center gap-3 text-[12.5px] whitespace-nowrap text-muted max-xl:hidden">
          <ModeBadge mode={mode} />
          {dataset?.namespace ? (
            <span title="Kubernetes namespace">
              <span class="text-faint">Namespace</span> <span class="font-mono text-fg">{dataset.namespace}</span>
            </span>
          ) : null}
          {dataset?.version ? <span class="font-mono">{dataset.version}</span> : null}
        </div>
        <div class="ml-auto flex items-center gap-3 max-md:hidden">
          {mode === "demo" ? <PersonaSwitcher persona={persona} onChange={onPersonaChange} /> : null}
          {signedIn ? <UserMenu name={userName} adminActions={adminActions} adminBusy={adminBusy} onAdmin={onAdmin} /> : null}
          <ThemeToggle mode={themeMode} onToggle={onToggleTheme} />
        </div>
        <button
          ref={menuButton}
          type="button"
          class="ml-auto grid size-10 cursor-pointer place-items-center rounded-lg border-0 bg-transparent text-fg hover:bg-surface-2 md:hidden"
          aria-expanded={menuOpen}
          aria-controls="compact-menu"
          aria-label={menuOpen ? "Close menu" : "Open menu"}
          onClick={() => setMenuOpen((o) => !o)}
        >
          <Icon name={menuOpen ? "x" : "menu"} size={20} />
        </button>
      </div>

      {menuOpen ? (
        <div
          id="compact-menu"
          ref={panel}
          class="max-h-[calc(100dvh-61px)] overflow-y-auto border-t border-line bg-page md:hidden"
        >
          <div class="mx-auto flex w-[calc(100%-32px)] flex-col gap-4 py-4">
            <nav aria-label="Views">
              <ul class="m-0 flex list-none flex-col gap-0.5 p-0">
                {views.map((v) => (
                  <li key={v.label}>
                    <a
                      href={v.href}
                      aria-current={v.active ? "page" : undefined}
                      class={`flex min-h-12 items-center rounded-lg px-3 text-[16px] font-semibold no-underline ${
                        v.active ? "bg-[color-mix(in_oklab,var(--patchy-fg)_8%,transparent)] text-fg" : "text-muted hover:bg-surface-2 hover:text-fg"
                      }`}
                    >
                      {v.label}
                    </a>
                  </li>
                ))}
              </ul>
            </nav>

            {mode === "demo" ? (
              <div class="border-t border-line pt-4">
                <PersonaSwitcher persona={persona} onChange={onPersonaChange} />
              </div>
            ) : null}

            <div class="flex flex-col gap-0.5 border-t border-line pt-3">
              {signedIn ? (
                <>
                  <p class="m-0 px-2.5 pb-1 text-[13px] text-muted">
                    Signed in as <span class="font-semibold text-fg">{userName}</span>
                  </p>
                  <AccountActions adminActions={adminActions} adminBusy={adminBusy} onAdmin={onAdmin} onDone={() => setMenuOpen(false)} menu={false} />
                </>
              ) : null}
              <button type="button" class={MENU_ITEM} onClick={onToggleTheme}>
                <Icon name={themeMode === "dark" ? "sun" : "moon"} size={15} class="flex-none text-muted" />
                {themeMode === "dark" ? "Use the light theme" : "Use the dark theme"}
              </button>
            </div>

            <div class="flex flex-wrap items-center gap-3 border-t border-line px-2.5 pt-3 text-[12.5px] text-muted">
              <ModeBadge mode={mode} />
              {dataset?.namespace ? (
                <span>
                  <span class="text-faint">Namespace</span> <span class="font-mono text-fg">{dataset.namespace}</span>
                </span>
              ) : null}
              {dataset?.version ? <span class="font-mono">{dataset.version}</span> : null}
            </div>
          </div>
        </div>
      ) : null}
    </header>
  );
}
