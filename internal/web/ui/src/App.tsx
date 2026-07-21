import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import {
  AuthRequiredError,
  ForbiddenError,
  dataMode,
  fetchFindings,
  fetchRollups,
  postAction,
  subscribe,
} from "./api";
import { consumeLogoutMarker, readAuthError, readProvider, signInURL } from "./auth";
import type { ActionVerb, Dataset, Phase } from "./types";
import { emptySelection, filterFindings, repoOptions, sortFindings, type Selection } from "./filters";
import { useRoute } from "./router";
import { DEFAULT_PERSONA, type Persona } from "./mock/personas";
import { FilterBar } from "./components/FilterBar";
import { FindingDetail, MissingFinding } from "./components/FindingDetail";
import { FindingsTable } from "./components/FindingsTable";
import { PhasePipeline } from "./components/PhasePipeline";
import { RollupsView } from "./components/RollupsView";
import { StatTiles } from "./components/StatTiles";
import { Toasts, type ToastItem } from "./components/Toast";
import { TopBar } from "./components/TopBar";
import { useBodyThemeMode } from "./components/ThemeToggle";

const TOAST_DISMISS_MS = 6000;

export function App() {
  const mode = dataMode();
  const route = useRoute();
  const [themeMode, toggleTheme] = useBodyThemeMode();
  const [dataset, setDataset] = useState<Dataset | null>(null);
  const [error, setError] = useState<string | null>(null);
  // findingsBlocked is set when /api/findings said 401 ("unauthenticated")
  // or 403 (the denial message); rollups keep rendering either way.
  const [findingsBlocked, setFindingsBlocked] = useState<string | null>(null);
  const [authError] = useState<string | null>(() => readAuthError());
  const [persona, setPersona] = useState<Persona>(DEFAULT_PERSONA);
  const [busy, setBusy] = useState<ActionVerb | null>(null);
  const [selection, setSelection] = useState<Selection>(emptySelection);
  const [toasts, setToasts] = useState<ToastItem[]>([]);
  const toastId = useRef(0);

  const pushToast = useCallback((message: string, tone: ToastItem["tone"]) => {
    const id = ++toastId.current;
    setToasts((t) => [...t, { id, message, tone }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), TOAST_DISMISS_MS);
  }, []);

  const load = useCallback(async (p: Persona) => {
    try {
      const data = await fetchFindings(p);
      setDataset(data);
      setError(null);
      setFindingsBlocked(null);
    } catch (e) {
      if (e instanceof AuthRequiredError || e instanceof ForbiddenError) {
        setFindingsBlocked(e instanceof AuthRequiredError ? "unauthenticated" : e.message);
        // The statistics surface is public regardless; fall back to it so
        // the rollups view keeps working.
        try {
          setDataset(await fetchRollups());
          setError(null);
        } catch {
          // Keep whatever we had; the panel explains the findings gate.
        }
      } else {
        setError(e instanceof Error ? e.message : String(e));
      }
    }
  }, []);

  useEffect(() => {
    void load(persona);
  }, [load, persona]);

  // autoLogin: bounce straight to the provider when the server asks for it,
  // unless sign-in just failed or the user just signed out.
  useEffect(() => {
    if (findingsBlocked !== "unauthenticated") return;
    const provider = readProvider();
    if (provider?.autoLogin && !provider.authenticated && !authError && !consumeLogoutMarker()) {
      location.href = signInURL();
    }
  }, [findingsBlocked, authError]);

  useEffect(() => subscribe(() => void load(persona)), [load, persona]);

  const onAction = useCallback(
    async (name: string, verb: ActionVerb) => {
      setBusy(verb);
      try {
        await postAction(name, verb, persona);
        pushToast(`${verb} applied to ${name}.`, "green");
        await load(persona);
      } catch (e) {
        if (e instanceof AuthRequiredError) {
          setFindingsBlocked("unauthenticated");
        } else if (e instanceof ForbiddenError) {
          pushToast(e.message, "red");
        } else {
          pushToast(e instanceof Error ? e.message : String(e), "red");
        }
      } finally {
        setBusy(null);
      }
    },
    [load, persona, pushToast],
  );

  const simulate403 = useCallback(() => {
    pushToast(
      `Permission denied. User "${persona.label}" does not have access to this action (simulated).`,
      "red",
    );
  }, [persona, pushToast]);

  const togglePhase = useCallback((phase: Phase) => {
    setSelection((sel) => {
      const phases = new Set(sel.phases);
      phases.has(phase) ? phases.delete(phase) : phases.add(phase);
      return { ...sel, phases };
    });
  }, []);

  const findings = dataset?.findings ?? [];
  const visible = useMemo(
    () => sortFindings(filterFindings(findings, selection)),
    [findings, selection],
  );
  const repos = useMemo(() => repoOptions(findings), [findings]);

  const panel = (title: string, detail: string, actions?: ComponentChildren) => (
    <div class="mx-auto my-20 max-w-[460px] rounded-xl border border-line-2 bg-surface p-7 text-center shadow-card">
      <h1 class="mx-0 mt-0 mb-2 text-[19px] tracking-tight">{title}</h1>
      <p class="mx-0 mt-0 mb-4.5 text-muted">{detail}</p>
      {authError ? <p class="mx-0 mt-0 mb-4.5 text-[13px] text-red">{authError}</p> : null}
      <div class="flex justify-center gap-2">
        {actions}
        <button type="button" class="ps-action" onClick={() => void load(persona)}>
          Retry
        </button>
      </div>
    </div>
  );

  // authPanel explains why the findings surface is unavailable. With a
  // provider it offers sign-in; without one (the server has no auth config)
  // it says so — no dead buttons.
  const authPanel = () => {
    if (findingsBlocked !== "unauthenticated") {
      return panel("Permission denied", findingsBlocked ?? "");
    }
    const provider = readProvider();
    if (!provider) {
      return panel(
        "Sign-in is not configured",
        "Findings require authentication, and this server has no sign-in configured. " +
          "Rollup statistics remain available from the Rollups view.",
      );
    }
    return panel(
      "Authentication required",
      "Sign in to view findings in this namespace.",
      <a class="ps-action ps-action--primary no-underline" href={signInURL()}>
        Sign in
      </a>,
    );
  };

  let body;
  if (findingsBlocked !== null && route.view !== "rollups") {
    body = authPanel();
  } else if (error && !dataset) {
    body = panel("Cannot reach the status API", error);
  } else if (!dataset) {
    body = <div class="px-5 py-11 text-center text-muted">Loading…</div>;
  } else if (route.view === "rollups") {
    body = <RollupsView rollups={dataset.rollups ?? []} scope={route.scope} />;
  } else if (route.view === "detail") {
    const finding = findings.find((f) => f.name === route.name);
    body = finding ? (
      <FindingDetail
        finding={finding}
        tab={route.tab}
        demo={mode === "demo"}
        busy={busy}
        onAction={(verb) => void onAction(finding.name, verb)}
        onSimulate403={simulate403}
      />
    ) : (
      <MissingFinding name={route.name} />
    );
  } else {
    body = (
      <>
        <StatTiles findings={findings} />
        <PhasePipeline findings={findings} selected={selection.phases} onToggle={togglePhase} />
        <FilterBar selection={selection} repos={repos} onChange={setSelection} />
        <FindingsTable findings={visible} />
      </>
    );
  }

  return (
    <>
      <TopBar
        dataset={dataset}
        mode={mode}
        route={route}
        themeMode={themeMode}
        onToggleTheme={toggleTheme}
        persona={persona}
        onPersonaChange={setPersona}
      />
      <main class="mx-auto w-[min(1240px,calc(100%-40px))] pt-6 pb-20 max-sm:w-[calc(100%-28px)]">{body}</main>
      <Toasts toasts={toasts} onDismiss={(id) => setToasts((t) => t.filter((x) => x.id !== id))} />
    </>
  );
}
