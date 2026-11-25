import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import {
  AuthRequiredError,
  ForbiddenError,
  dataMode,
  fetchConfig,
  fetchFinding,
  fetchRollups,
  postAction,
  postAdmin,
  postBackfill,
  subscribe,
  subscribeConfig,
} from "./api";
import { consumeLogoutMarker, readAuthError, readProvider, signInURL, signOut } from "./auth";
import type { ActionVerb, AdminVerb, ConfigDataset, DatasetHeader, Finding, FindingsPage, Phase } from "./types";
import { emptySelection, isFiltered, selectionFromSearch, selectionSearch, type Selection } from "./filters";
import { useFindingsList } from "./findingsList";
import { hrefForList, parseRoute, setListQuery, useRoute } from "./router";
import { DEFAULT_PERSONA, type Persona } from "./mock/personas";
import { ConfigView } from "./components/ConfigView";
import { FindingDetail, MissingFinding } from "./components/FindingDetail";
import { FindingsTable } from "./components/FindingsTable";
import { FindingsOverview } from "./components/FindingsOverview";
import { FindingsFilter } from "./components/FindingsFilter";
import { MetricsView } from "./components/MetricsView";
import { RepositoriesView } from "./components/RepositoriesView";
import { Toasts, type ToastItem } from "./components/Toast";
import { TopBar } from "./components/TopBar";
import { useBodyThemeMode } from "./components/ThemeToggle";

const TOAST_DISMISS_MS = 6000;

// replaceHash points the URL at href without a history entry or a
// hashchange. No hash at all, "#" and "#/" are the same unfiltered list.
function replaceHash(href: string): void {
  if (location.hash.replace(/^#?\/?/, "#/") !== href) history.replaceState(null, "", href);
}

export function App() {
  const mode = dataMode();
  const route = useRoute();
  const [themeMode, toggleTheme] = useBodyThemeMode();
  // dataset is the header the top bar renders (namespace, version, user),
  // from the findings list's first page or, failing that, the metrics summary.
  const [dataset, setDataset] = useState<DatasetHeader | null>(null);
  const [error, setError] = useState<string | null>(null);
  // findingsBlocked is set when /api/findings said 401 ("unauthenticated")
  // or 403 (the denial message). Unauthenticated blocks every view; a 403
  // on findings still lets the metrics view try its own gate.
  const [findingsBlocked, setFindingsBlocked] = useState<string | null>(null);
  const [authError] = useState<string | null>(() => readAuthError());
  const [persona, setPersona] = useState<Persona>(DEFAULT_PERSONA);
  const [busy, setBusy] = useState<ActionVerb | null>(null);
  const [selection, setSelection] = useState<Selection>(() =>
    route.view === "list" ? selectionFromSearch(route.query) : emptySelection(),
  );
  const [toasts, setToasts] = useState<ToastItem[]>([]);
  const toastId = useRef(0);

  const pushToast = useCallback((message: string, tone: ToastItem["tone"]) => {
    const id = ++toastId.current;
    setToasts((t) => [...t, { id, message, tone }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), TOAST_DISMISS_MS);
  }, []);

  const onPage = useCallback(({ generatedAt, namespace, version, user }: FindingsPage) => {
    setDataset({ generatedAt, namespace, version, user });
    setError(null);
    setFindingsBlocked(null);
  }, []);
  const onListFailure = useCallback(async (e: unknown) => {
    if (e instanceof AuthRequiredError || e instanceof ForbiddenError) {
      setFindingsBlocked(e instanceof AuthRequiredError ? "unauthenticated" : e.message);
      // Signed in but not allowed findings: the metrics summary may still
      // be allowed (its own grant), and carries the user and namespace the
      // top bar renders.
      if (e instanceof ForbiddenError) {
        try {
          setDataset(await fetchRollups());
          setError(null);
        } catch {
          // Not allowed metrics either; the views explain their gates.
        }
      }
    } else {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);
  const isAuthError = useCallback(
    (e: unknown) => e instanceof AuthRequiredError || e instanceof ForbiddenError,
    [],
  );

  // The filter is in the list route's query string (#/?severity=high&…), so
  // a shared or saved link restores it. Links back to the list carry it
  // (recorded here, before the children render theirs).
  const search = selectionSearch(selection);
  setListQuery(search);
  // Opening a link, or a history step, onto the list applies its filter,
  // and settles the URL on the canonical form (unknown values dropped).
  useEffect(() => {
    if (route.view !== "list") return;
    const next = selectionFromSearch(route.query);
    setSelection((sel) => (selectionSearch(sel) === selectionSearch(next) ? sel : next));
    replaceHash(hrefForList(selectionSearch(next)));
  }, [route]);
  // A filter change rewrites the list's URL in place: no history entry (and
  // no hashchange) per chip.
  useEffect(() => {
    if (parseRoute(location.hash).view === "list") replaceHash(hrefForList(search));
  }, [search]);

  // The findings list, a page at a time. It loads on every route: its first
  // page also carries the header the top bar renders.
  const list = useFindingsList(selection, persona, onPage, onListFailure, isAuthError);
  // load refetches the list's first page. Its identity is stable (it reads
  // the latest refresh through a ref), so the SSE subscription below is not
  // reopened on every filter change.
  const refreshList = useRef(list.refresh);
  refreshList.current = list.refresh;
  const load = useCallback(() => refreshList.current(), []);

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
  // config-changed signals, but only while the view is active, so idle
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
          `Backfill requested on ${integration}. The controller lists open alerts on its next reconcile.`,
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

  // One findings-changed signal refetches the list's first page and, when a
  // detail view is open, that one finding. Both requests are cheap: a page
  // is bounded and the detail is a single object.
  useEffect(
    () =>
      subscribe(() => {
        load();
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
        load();
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
            ? "Replay requested. The integration will redeliver the webhook log shortly."
            : "All pipeline data deleted.",
          "green",
        );
        load();
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

  // The repositories page reports a lost session or a missing grant the
  // way every other view does.
  const onRepositoriesAuthError = useCallback((e: AuthRequiredError | ForbiddenError) => {
    setFindingsBlocked(e instanceof AuthRequiredError ? "unauthenticated" : e.message);
  }, []);

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


  const panel = (title: string, detail: string, actions?: ComponentChildren) => (
    <div class="mx-auto my-20 max-w-[460px] rounded-[10px] border border-line-2 bg-surface p-7 text-center shadow-card">
      <h1 class="mx-0 mt-0 mb-2 text-[19px] tracking-tight">{title}</h1>
      <p class="mx-0 mt-0 mb-4.5 text-muted">{detail}</p>
      {authError ? <p class="mx-0 mt-0 mb-4.5 text-[13px] text-red">{authError}</p> : null}
      <div class="flex justify-center gap-2">
        {actions}
        <button type="button" class="ps-action" onClick={load}>
          Retry
        </button>
      </div>
    </div>
  );

  // authPanel explains why the findings surface is unavailable. With a
  // provider it offers sign-in; without one (the server has no auth config)
  // it says so rather than showing a dead button.
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
        "This status server has no authentication configured, so it shows nothing. " +
          "Configure sign-in (or explicitly disable authentication) to use it.",
      );
    }
    return panel(
      "Authentication required",
      "Sign in to view findings and metrics in this namespace.",
      <a class="ps-action ps-action--primary no-underline" href={signInURL()}>
        Sign in
      </a>,
    );
  };

  let body;
  if (findingsBlocked === "unauthenticated" || (findingsBlocked !== null && route.view !== "metrics")) {
    body = authPanel();
  } else if (route.view === "metrics") {
    // Loads and gates itself (its own grant), independent of the findings
    // dataset.
    body = <MetricsView slice={route.slice} />;
  } else if (error && !dataset) {
    body = panel("Cannot reach the status API", error);
  } else if (!dataset) {
    body = <div class="px-5 py-11 text-center text-muted">Loading…</div>;
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
  } else if (route.view === "repositories") {
    body = (
      <RepositoriesView
        persona={persona}
        demo={mode === "demo"}
        onToast={pushToast}
        onAuthError={onRepositoriesAuthError}
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
        <FindingsOverview
          facets={list.facets}
          selected={selection.phases}
          onToggle={togglePhase}
          onOnly={(phase) => setSelection((sel) => ({ ...sel, phases: new Set([phase]) }))}
        />
        <div class="mt-4">
          <FindingsFilter facets={list.facets} selection={selection} onChange={setSelection} />
        </div>
        <FindingsTable
          list={list}
          total={list.facets?.total ?? 0}
          filtered={isFiltered(selection)}
          onClear={() => setSelection(emptySelection())}
        />
      </>
    );
  }

  return (
    <>
      {/* Not href="#main": the hash is the router, so the link focuses
          main itself instead of navigating. */}
      <a
        class="ps-skip"
        href={location.hash || "#/"}
        onClick={(e) => {
          e.preventDefault();
          document.getElementById("main")?.focus();
        }}
      >
        Skip to content
      </a>
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
      <main id="main" tabIndex={-1} class="mx-auto w-[min(1240px,calc(100%-40px))] pt-7 pb-20 outline-none max-sm:w-[calc(100%-32px)]">
        {body}
      </main>
      <Toasts toasts={toasts} onDismiss={(id) => setToasts((t) => t.filter((x) => x.id !== id))} />
    </>
  );
}
