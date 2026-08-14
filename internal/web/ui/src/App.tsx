import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import {
  AuthRequiredError,
  ForbiddenError,
  dataMode,
  fetchConfig,
  fetchFinding,
  fetchFindings,
  fetchRollups,
  postAction,
  postAdmin,
  postBackfill,
  subscribe,
  subscribeConfig,
} from "./api";
import { consumeLogoutMarker, readAuthError, readProvider, signInURL, signOut } from "./auth";
import type { ActionVerb, AdminVerb, ConfigDataset, Dataset, Finding, Phase } from "./types";
import { emptySelection, filterFindings, repoOptions, sortFindings, type Selection } from "./filters";
import { useRoute } from "./router";
import { DEFAULT_PERSONA, type Persona } from "./mock/personas";
import { ConfigView } from "./components/ConfigView";
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

  // The detail view lazily fetches its finding: the list payload carries
  // trimmed summaries only. detail is null while loading or when the finding
  // has expired; detailMissing separates the two.
  const detailName = route.view === "detail" ? route.name : null;
  const [detail, setDetail] = useState<Finding | null>(null);
  const [detailMissing, setDetailMissing] = useState(false);
  const detailRoute = useRef<string | null>(null);
  const loadDetail = useCallback(async (name: string, p: Persona) => {
    try {
      const f = await fetchFinding(name, p);
      // A slow response for a finding the user has navigated away from must
      // not clobber the one now open.
      if (detailRoute.current !== name) return;
      setDetail(f);
      setDetailMissing(!f);
    } catch (e) {
      if (e instanceof AuthRequiredError || e instanceof ForbiddenError) {
        setFindingsBlocked(e instanceof AuthRequiredError ? "unauthenticated" : e.message);
      } else {
        setError(e instanceof Error ? e.message : String(e));
      }
    }
  }, []);
  useEffect(() => {
    detailRoute.current = detailName;
    setDetail(null);
    setDetailMissing(false);
    if (detailName) void loadDetail(detailName, persona);
  }, [detailName, loadDetail, persona]);

  // The configuration view lazily fetches its own dataset and refetches on
  // config-changed signals — but only while the view is active, so idle
  // sessions cost nothing.
  const configActive = route.view === "config";
  const [config, setConfig] = useState<ConfigDataset | null>(null);
  const loadConfig = useCallback(async () => {
    try {
      setConfig(await fetchConfig());
    } catch (e) {
      if (e instanceof AuthRequiredError || e instanceof ForbiddenError) {
        setFindingsBlocked(e instanceof AuthRequiredError ? "unauthenticated" : e.message);
      } else {
        setError(e instanceof Error ? e.message : String(e));
      }
    }
  }, []);
  useEffect(() => {
    if (!configActive) return;
    void loadConfig();
    return subscribeConfig(() => void loadConfig());
  }, [configActive, loadConfig]);

  const [backfillBusy, setBackfillBusy] = useState<string | null>(null);
  const onBackfill = useCallback(
    async (integration: string, repos: string[]) => {
      setBackfillBusy(integration);
      try {
        await postBackfill(integration, repos, persona);
        pushToast(
          `Backfill requested on ${integration} — the controller lists open alerts on its next reconcile.`,
          "green",
        );
        await loadConfig();
      } catch (e) {
        if (e instanceof AuthRequiredError) {
          setFindingsBlocked("unauthenticated");
        } else {
          pushToast(e instanceof Error ? e.message : String(e), "red");
        }
      } finally {
        setBackfillBusy(null);
      }
    },
    [loadConfig, persona, pushToast],
  );

  // autoLogin: bounce straight to the provider when the server asks for it,
  // unless sign-in just failed or the user just signed out.
  useEffect(() => {
    if (findingsBlocked !== "unauthenticated") return;
    const provider = readProvider();
    if (provider?.autoLogin && !provider.authenticated && !authError && !consumeLogoutMarker()) {
      location.href = signInURL();
    }
  }, [findingsBlocked, authError]);

  // One findings-changed signal refetches the list and, when a detail view
  // is open, that one finding — both requests are cheap now that the list is
  // trimmed and the detail is a single object.
  useEffect(
    () =>
      subscribe(() => {
        void load(persona);
        if (detailName) void loadDetail(detailName, persona);
      }),
    [load, loadDetail, detailName, persona],
  );

  const onAction = useCallback(
    async (name: string, verb: ActionVerb) => {
      setBusy(verb);
      try {
        await postAction(name, verb, persona);
        pushToast(`${verb} applied to ${name}.`, "green");
        await load(persona);
        if (detailRoute.current) await loadDetail(detailRoute.current, persona);
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
    [load, loadDetail, persona, pushToast],
  );

  const [adminBusy, setAdminBusy] = useState<AdminVerb | null>(null);
  const onAdmin = useCallback(
    async (verb: AdminVerb) => {
      setAdminBusy(verb);
      try {
        await postAdmin(verb);
        pushToast(
          verb === "replay"
            ? "Replay requested — the integration will redeliver the webhook log shortly."
            : "All pipeline data deleted.",
          "green",
        );
        await load(persona);
      } catch (e) {
        if (e instanceof AuthRequiredError) {
          setFindingsBlocked("unauthenticated");
        } else {
          pushToast(e instanceof Error ? e.message : String(e), "red");
        }
      } finally {
        setAdminBusy(null);
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

  // Live mode takes the server-resolved grants; demo mode previews the
  // persona's. The nav link and the trigger render only when granted.
  const configView = mode === "demo" ? persona.configView : Boolean(dataset?.user?.configView);
  const integrationActions =
    mode === "demo" ? persona.integrationActions : (dataset?.user?.integrationActions ?? []);

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
      // Signed in but not authorized: offer sign-out so the user can switch
      // to an account that is.
      return panel(
        "Permission denied",
        findingsBlocked ?? "",
        readProvider()?.authenticated ? (
          <button type="button" class="ps-action" onClick={() => void signOut()}>
            Sign out
          </button>
        ) : undefined,
      );
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
  } else if (route.view === "config") {
    body = (
      <ConfigView
        config={config}
        section={route.section}
        canBackfill={integrationActions.includes("backfill")}
        backfillBusy={backfillBusy}
        onBackfill={(name, repos) => void onBackfill(name, repos)}
      />
    );
  } else if (route.view === "detail") {
    if (detailMissing) {
      body = <MissingFinding name={route.name} />;
    } else if (!detail) {
      body = <div class="px-5 py-11 text-center text-muted">Loading…</div>;
    } else {
      const finding = detail;
      body = (
        <FindingDetail
          finding={finding}
          tab={route.tab}
          demo={mode === "demo"}
          busy={busy}
          onAction={(verb) => void onAction(finding.name, verb)}
          onSimulate403={simulate403}
        />
      );
    }
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
        configView={configView}
        themeMode={themeMode}
        onToggleTheme={toggleTheme}
        persona={persona}
        onPersonaChange={setPersona}
        adminBusy={adminBusy}
        onAdmin={(verb) => void onAdmin(verb)}
      />
      <main class="mx-auto w-[min(1240px,calc(100%-40px))] pt-6 pb-20 max-sm:w-[calc(100%-28px)]">{body}</main>
      <Toasts toasts={toasts} onDismiss={(id) => setToasts((t) => t.filter((x) => x.id !== id))} />
    </>
  );
}
