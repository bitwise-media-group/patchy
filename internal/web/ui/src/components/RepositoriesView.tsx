// The repositories page: the repositories this browser tracks, each drawn
// as its findings' position on patchy's pipeline (every count opens the
// findings list filtered to it). A repository with nothing in the pipeline
// gets one sentence instead: its history, or, when patchy has never seen it,
// whether patchy can read its alerts. Any row can be backfilled once per
// cooldown window: at once on the backfill grant (or, for a maintainer with
// a connected GitHub account, on repositories they maintain), or as a
// request an operator decides on this same page. The list lives in
// localStorage (trackedRepos.ts); the server never learns it beyond the
// names a request carries. A connected GitHub account can fill it: the
// import picker lists what the account maintains, and a refresh drops the
// imported entries it no longer does.

import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import {
  AuthRequiredError,
  ForbiddenError,
  connectGitHub,
  disconnectGitHub,
  fetchBackfillRequests,
  fetchGitHubRepositories,
  fetchRepositories,
  fetchRepositoryAccess,
  postBackfillDecision,
  postRepositoryBackfill,
  queryRepositories,
  subscribe,
  subscribeConfig,
  suggestFindings,
} from "../api";
import type { Persona } from "../mock/personas";
import { DEMO_TRACKED } from "../mock/repositories";
import { ownerName, shortName } from "../rollups";
import {
  PHASE_GROUPS,
  accessProblems,
  accessReason,
  anyConnected,
  backfillAvailable,
  backfillMode,
  backfillOpen,
  backfillOutcomeMessage,
  backfillState,
  filterImportable,
  githubProfileHref,
  findingsHref,
  findingsHrefFor,
  forgeBadge,
  forgeProblem,
  githubConnectHref,
  groupCount,
  metricsHref,
  neverSeen,
  ownerGroups,
  type BackfillMode,
  type GroupTone,
  type PhaseGroup,
} from "../repositories";
import {
  MAX_TRACKED,
  STORAGE_KEY,
  add,
  defaultStore,
  exportJSON,
  importGitHub,
  importJSON,
  normalizeRepoName,
  refreshGitHub,
  remove,
  room,
  save,
  tracks,
  useTrackedRepos,
  type KeyValueStore,
  type TrackedRepo,
} from "../trackedRepos";
import type {
  BackfillRequest,
  BackfillRequestsPayload,
  GitHubLink,
  GitHubRepositoriesPayload,
  GitHubRepository,
  Phase,
  RepositoriesPayload,
  RepositoryBackfill,
  RepositoryAccess,
  RepositoryAccessPayload,
  RepositorySummary,
} from "../types";
import { formatAgo, formatCount, formatIn, plural } from "../format";
import { hrefForRepositories } from "../router";
import { ConfirmButton } from "./ConfirmButton";
import { rank } from "../typeahead";
import { FilterBox, Highlight, type FilterGroup, type FilterOption } from "./FilterBox";
import { Icon } from "./icons";
import { Pill } from "./Pills";
import { BackfillButton } from "./BackfillButton";

const SUGGESTIONS = 6;
const DEBOUNCE_MS = 150;
// ACCESS_BATCH is the server's cap on one access check.
const ACCESS_BATCH = 20;

type Tone = "green" | "red";

// The pipeline's stations: the five a finding moves through, then the two
// it can leave by. Every row shares one grid so the stations line up into
// columns: name, five stations, a gap, two terminal stations, remove.
const TRACK = PHASE_GROUPS.filter((g) => g.id !== "failed" && g.id !== "closed");
const TERMINAL = PHASE_GROUPS.filter((g) => g.id === "failed" || g.id === "closed");
const ROW_GRID =
  "md:grid md:grid-cols-[minmax(11rem,1.4fr)_repeat(5,minmax(3.5rem,1fr))_1.25rem_repeat(2,minmax(3.5rem,0.85fr))_2.25rem] md:items-center md:gap-x-0";

// A station holding findings: the count on a disc tinted with its tone,
// opaque over the track line. Written out whole so Tailwind sees each class.
const STATION: Record<GroupTone, string> = {
  soil: "bg-[color-mix(in_oklab,var(--patchy-soil)_24%,var(--patchy-surface))] text-soil",
  amber: "bg-[color-mix(in_oklab,var(--patchy-amber)_24%,var(--patchy-surface))] text-amber",
  seedling: "bg-[color-mix(in_oklab,var(--patchy-seedling)_22%,var(--patchy-surface))] text-seedling",
  green: "bg-[color-mix(in_oklab,var(--patchy-green)_22%,var(--patchy-surface))] text-ink",
  red: "bg-[color-mix(in_oklab,var(--patchy-red)_22%,var(--patchy-surface))] text-red",
  faint: "bg-surface-2 text-muted",
};

const IN_PIPELINE: Phase[] = TRACK.flatMap((g) => g.phases);

// demoStore seeds the demo's list the first time, so the demo (and the
// release screenshots, shot in a fresh browser) open on a full page.
function demoStore(): KeyValueStore | null {
  const store = defaultStore();
  if (!store) return store;
  try {
    if (store.getItem(STORAGE_KEY) === null) {
      save(
        DEMO_TRACKED.map((name) => ({ name, addedAt: "2026-07-21T08:00:00Z" })),
        store,
      );
    }
  } catch {
    // Storage that cannot be read is handled by the hook.
  }
  return store;
}

// ago reads a recent time the way a sentence would: "just now" rather than
// "0s ago".
function ago(iso: string): string {
  return Date.now() - new Date(iso).getTime() < 60_000 ? "just now" : formatAgo(iso);
}


// RowBackfill is what a row's backfill control needs: what the caller may
// do, whether the cooldown allows it now, and the handler.
interface RowBackfill {
  mode: BackfillMode;
  available: boolean;
  availableAt?: string;
  busy: boolean;
  onBackfill: (request: boolean) => void;
}

// GitHubFetch is one Integration's answer to the import picker or a refresh.
type GitHubFetch = { integration: string; ok: true; payload: GitHubRepositoriesPayload } | { integration: string; ok: false; error: string };

async function fetchAllGitHub(links: GitHubLink[], signal?: AbortSignal): Promise<GitHubFetch[]> {
  return Promise.all(
    links
      .filter((l) => l.connected)
      .map((l) =>
        fetchGitHubRepositories(l.integration, signal).then(
          (payload): GitHubFetch => ({ integration: l.integration, ok: true, payload }),
          (e: unknown): GitHubFetch => ({
            integration: l.integration,
            ok: false,
            error: e instanceof Error ? e.message : String(e),
          }),
        ),
      ),
  );
}

// githubBack is where a GitHub connection returns to: this page.
function githubBack(): string {
  return location.pathname + location.search + hrefForRepositories();
}

export function RepositoriesView({
  persona,
  demo,
  onToast,
  onAuthError,
}: {
  persona: Persona;
  demo: boolean;
  onToast: (message: string, tone: Tone) => void;
  onAuthError: (e: AuthRequiredError | ForbiddenError) => void;
}) {
  const store = useMemo(() => (demo ? demoStore() : defaultStore()), [demo]);
  const tracked = useTrackedRepos(store);
  const names = useMemo(() => tracked.repos.map((r) => r.name), [tracked.repos]);
  const namesKey = names.join("\n");

  const [summary, setSummary] = useState<RepositoriesPayload | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const load = useCallback(async () => {
    try {
      const out = await fetchRepositories(names, persona);
      setSummary(out);
      setLoadError(null);
    } catch (e) {
      if (e instanceof AuthRequiredError || e instanceof ForbiddenError) onAuthError(e);
      else setLoadError(e instanceof Error ? e.message : String(e));
    }
    // names is captured through namesKey.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [namesKey, persona, onAuthError]);
  useEffect(() => void load(), [load]);

  // The decision queue is for holders of the backfill grant only (the same
  // grant the server's canDecide reports), so nobody else fetches it. Their
  // own requests show on their rows.
  const canDecide = !!summary?.canBackfill;
  const [requests, setRequests] = useState<BackfillRequestsPayload | null>(null);
  const loadRequests = useCallback(async () => {
    if (!canDecide) {
      setRequests(null);
      return;
    }
    try {
      setRequests(await fetchBackfillRequests(persona));
    } catch (e) {
      if (e instanceof AuthRequiredError) onAuthError(e);
      // Anything else leaves the panel as it was; the rows still work.
    }
  }, [canDecide, persona, onAuthError]);
  useEffect(() => void loadRequests(), [loadRequests]);

  const refreshAll = useCallback(() => Promise.all([load(), loadRequests()]), [load, loadRequests]);
  // Counts move as findings move: refetch on the findings-changed signal.
  const loadRef = useRef(refreshAll);
  loadRef.current = refreshAll;
  useEffect(() => subscribe(() => void loadRef.current()), []);
  // A backfill's progress lives on its BackfillRequest, not on any finding:
  // a walk that ingests nothing changes no finding, so listen for that too.
  useEffect(() => subscribeConfig(() => void loadRef.current()), []);

  const byName = useMemo(() => {
    const m = new Map<string, RepositorySummary>();
    for (const r of summary?.repositories ?? []) m.set(r.name, r);
    return m;
  }, [summary]);

  // The access check runs only for repositories patchy has never seen, and
  // only when that set changes: the server caches verdicts for minutes, and
  // nothing on this page changes them faster.
  const unseen = useMemo(() => names.filter((n) => neverSeen(byName.get(n))), [names, byName]);
  const unseenKey = unseen.join("\n");
  const [access, setAccess] = useState<Map<string, RepositoryAccess>>(new Map());
  const [accessState, setAccessState] = useState<{ available: boolean; error?: string } | null>(null);
  const checkAccess = useCallback(async (repos: string[], signal?: AbortSignal) => {
    const merged: RepositoryAccessPayload = { available: true, repositories: [] };
    for (let i = 0; i < repos.length; i += ACCESS_BATCH) {
      const page = await fetchRepositoryAccess(repos.slice(i, i + ACCESS_BATCH), signal);
      merged.available = page.available;
      merged.error = merged.error ?? page.error;
      merged.repositories.push(...page.repositories);
      if (!page.available) break;
    }
    return merged;
  }, []);
  useEffect(() => {
    if (!summary?.accessCheck || unseen.length === 0) {
      setAccessState(summary && !summary.accessCheck ? { available: false } : null);
      return;
    }
    const ctl = new AbortController();
    checkAccess(unseen, ctl.signal)
      .then((out) => {
        setAccess(new Map(out.repositories.map((r) => [r.name, r])));
        setAccessState({ available: out.available, error: out.error });
      })
      .catch((e: unknown) => {
        if (ctl.signal.aborted) return;
        if (e instanceof AuthRequiredError || e instanceof ForbiddenError) onAuthError(e);
        else setAccessState({ available: true, error: e instanceof Error ? e.message : String(e) });
      });
    return () => ctl.abort();
    // unseen is captured through unseenKey.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [unseenKey, summary?.accessCheck, checkAccess, onAuthError]);

  // refused holds the rows whose maintained backfill the server turned
  // down (the GitHub role is too low): they offer a request instead.
  const [refused, setRefused] = useState<Set<string>>(new Set());
  useEffect(() => setRefused(new Set()), [persona]);
  const [busy, setBusy] = useState<string | null>(null);
  const onBackfill = useCallback(
    async (repo: string, request: boolean) => {
      setBusy(repo);
      try {
        const res = await postRepositoryBackfill(repo, request, persona);
        onToast(backfillOutcomeMessage(repo, res), "green");
        await refreshAll();
        // Demo mode has no change stream, and its stand-in controller
        // finishes the request a moment later: look once more then.
        if (demo) setTimeout(() => void loadRef.current(), 2000);
      } catch (e) {
        if (e instanceof AuthRequiredError) {
          onAuthError(e);
          return;
        }
        // 403 and 429 bodies are sentences for the person who clicked.
        onToast(e instanceof Error ? e.message : String(e), "red");
        if (e instanceof ForbiddenError && !request && !summary?.canBackfill && summary?.canRequestBackfill) {
          setRefused((s) => new Set(s).add(repo));
        }
      } finally {
        setBusy(null);
      }
    },
    [demo, refreshAll, onAuthError, onToast, persona, summary?.canBackfill, summary?.canRequestBackfill],
  );

  const [deciding, setDeciding] = useState<string | null>(null);
  const onDecide = useCallback(
    async (req: BackfillRequest, verdict: "approve" | "deny", reason: string) => {
      setDeciding(req.name);
      const what = req.repositories?.length ? req.repositories.join(", ") : `everything on ${req.integration}`;
      try {
        await postBackfillDecision(req.name, verdict, reason, persona);
        onToast(
          verdict === "approve"
            ? `Approved the backfill of ${what}. It runs on ${req.integration} shortly.`
            : `Denied the backfill of ${what}.`,
          "green",
        );
        await refreshAll();
        if (demo && verdict === "approve") setTimeout(() => void loadRef.current(), 2000);
      } catch (e) {
        if (e instanceof AuthRequiredError) onAuthError(e);
        else onToast(e instanceof Error ? e.message : String(e), "red");
      } finally {
        setDeciding(null);
      }
    },
    [demo, refreshAll, onAuthError, onToast, persona],
  );

  // ---- GitHub ----
  const links = summary?.github ?? [];
  const [ghBusy, setGhBusy] = useState<string | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  useEffect(() => {
    if (!anyConnected(summary?.github)) setImportOpen(false);
  }, [summary?.github]);
  const onConnect = useCallback(
    async (integration: string, connect: boolean) => {
      setGhBusy(integration);
      try {
        if (connect) await connectGitHub(integration, githubBack());
        else await disconnectGitHub(integration);
        if (!connect || demo) await load();
        if (!connect) onToast(`Disconnected GitHub from ${integration}.`, "green");
      } catch (e) {
        if (e instanceof AuthRequiredError) onAuthError(e);
        else onToast(e instanceof Error ? e.message : String(e), "red");
      } finally {
        setGhBusy(null);
      }
    },
    [demo, load, onAuthError, onToast],
  );
  const onImportPicked = useCallback(
    (picked: string[]) => {
      const res = importGitHub(tracked.repos, picked);
      tracked.update(res.repos);
      onToast(
        `Added ${plural(res.added, "repository", "repositories")} from GitHub` +
          (res.skipped ? `. Skipped ${res.skipped}: already listed, or past the limit of ${MAX_TRACKED}.` : "."),
        res.added ? "green" : "red",
      );
    },
    [tracked, onToast],
  );
  const onRefreshGitHub = useCallback(async () => {
    setGhBusy("refresh");
    try {
      const results = await fetchAllGitHub(links);
      const failed = results.find((r): r is Extract<GitHubFetch, { ok: false }> => !r.ok);
      if (failed) {
        onToast(`Could not refresh from GitHub. ${failed.integration}: ${failed.error}`, "red");
        return;
      }
      const lists = results.flatMap((r) => (r.ok ? [r.payload] : []));
      if (lists.some((l) => l.truncated)) {
        onToast("GitHub's list of your repositories was cut short, so the refresh removed nothing.", "red");
        return;
      }
      const res = refreshGitHub(
        tracked.repos,
        lists.flatMap((l) => l.repositories.map((r) => r.name)),
      );
      if (res.removed.length) tracked.update(res.repos);
      onToast(
        res.removed.length
          ? `Removed ${plural(res.removed.length, "repository", "repositories")} you no longer maintain: ${res.removed.join(", ")}.`
          : "Every repository you imported from GitHub is one you still maintain.",
        "green",
      );
    } finally {
      setGhBusy(null);
    }
  }, [links, tracked, onToast]);

  const addRepo = (input: string) => {
    const res = add(tracked.repos, input);
    if (res.error) {
      onToast(res.error, "red");
      return;
    }
    tracked.update(res.repos);
  };

  const fileInput = useRef<HTMLInputElement>(null);
  const onExport = () => {
    const blob = new Blob([exportJSON(tracked.repos)], { type: "application/json" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "patchy-repositories.json";
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  };
  const onImport = async (file: File) => {
    try {
      const res = importJSON(await file.text(), tracked.repos);
      tracked.update(res.repos);
      onToast(
        `Imported ${plural(res.added, "repository", "repositories")}` +
          (res.skipped ? `. Skipped ${res.skipped}: duplicates, invalid names, or past the limit of ${MAX_TRACKED}.` : "."),
        res.added ? "green" : "red",
      );
    } catch {
      onToast(`${file.name} is not a repository list. Choose a JSON file exported from this page.`, "red");
    }
  };

  const unreadable = unseen.filter((n) => {
    const a = access.get(n);
    return a && !a.skipped && !a.backfillIntegration;
  }).length;

  const rowBackfill = (name: string): RowBackfill => {
    const s = byName.get(name);
    return {
      mode: backfillMode(summary, refused.has(name)),
      available: backfillAvailable(s),
      availableAt: s?.backfillAvailableAt,
      busy: busy === name,
      onBackfill: (request) => void onBackfill(name, request),
    };
  };

  return (
    <div class="flex flex-col gap-5">
      <Headline names={names} summary={summary} unreadable={unreadable} />

      <div class="flex flex-col gap-3">
        <AddRepository tracked={names} onAdd={addRepo} full={tracked.repos.length >= MAX_TRACKED} />
        {links.length ? (
          <GitHubBar
            links={links}
            canBackfillMaintained={!!summary?.canBackfillMaintained}
            busy={ghBusy}
            importOpen={importOpen}
            onConnect={(i) => void onConnect(i, true)}
            onDisconnect={(i) => void onConnect(i, false)}
            onToggleImport={() => setImportOpen((o) => !o)}
            onRefresh={() => void onRefreshGitHub()}
          />
        ) : null}
        {importOpen && anyConnected(links) ? (
          <GitHubImport
            links={links}
            tracked={tracked.repos}
            onAdd={onImportPicked}
            onClose={() => setImportOpen(false)}
          />
        ) : null}
      </div>

      {(accessState && !accessState.available && unseen.length > 0) || accessState?.error || loadError ? (
        <p class="m-0 flex items-start gap-2 text-[13.5px] text-muted">
          <Icon name="alertTriangle" size={15} class="mt-0.5 flex-none text-amber" />
          {loadError
            ? `Your repositories could not be loaded: ${loadError}`
            : accessState?.error
              ? `${accessState.error} Rows for repositories patchy has never seen cannot say whether their alerts are readable.`
              : "The access check is turned off on this server, so rows for repositories patchy has never seen cannot say whether their alerts are readable."}
        </p>
      ) : null}

      <section class="ps-card" aria-label="Tracked repositories">
        {tracked.repos.length === 0 ? (
          <div class="px-6 py-12 text-center">
            <p class="m-0 text-[15px] font-semibold text-fg">No repositories yet</p>
            <p class="mx-auto mt-1.5 mb-0 max-w-[52ch] text-[13.5px] text-muted">
              Add the repositories you look after to see where their findings stand
              {anyConnected(links) ? ", import the ones you maintain from GitHub," : ""} or import a list you exported
              from another browser.
            </p>
          </div>
        ) : (
          <>
            <StationHeader />
            {ownerGroups(names).map(({ owner, repos }, i) => (
              <div key={owner} role="group" aria-label={owner}>
                <p class={`m-0 px-5 pt-3.5 pb-1 text-[12.5px] text-faint ${i > 0 ? "border-t border-line" : ""}`}>{owner}</p>
                <ul class="m-0 list-none p-0">
                  {repos.map((name) => (
                    <RepositoryRow
                      key={name}
                      name={name}
                      summary={byName.get(name)}
                      loaded={summary !== null}
                      access={access.get(name)}
                      accessPending={
                        !!summary?.accessCheck && neverSeen(byName.get(name)) && !access.has(name) && !accessState
                      }
                      backfill={rowBackfill(name)}
                      onRemove={() => tracked.update(remove(tracked.repos, name))}
                    />
                  ))}
                </ul>
              </div>
            ))}
          </>
        )}
      </section>

      <footer class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 text-[13px] text-muted">
        <p class="m-0">
          {tracked.persisted ? (
            "Your list is saved in this browser only. Export it to use it in another browser."
          ) : (
            <span class="text-red">This browser blocks site storage, so your list lasts only until you leave the page.</span>
          )}
        </p>
        <div class="flex gap-1">
          <button type="button" class={QUIET_BUTTON} onClick={() => fileInput.current?.click()}>
            Import
          </button>
          <button type="button" class={QUIET_BUTTON} onClick={onExport} disabled={tracked.repos.length === 0}>
            <Icon name="download" size={14} /> Export
          </button>
          <input
            ref={fileInput}
            type="file"
            accept="application/json,.json"
            class="hidden"
            onChange={(e) => {
              const input = e.target as HTMLInputElement;
              const file = input.files?.[0];
              if (file) void onImport(file);
              input.value = "";
            }}
          />
        </div>
      </footer>

      {requests?.canDecide ? (
        <BackfillRequests
          requests={requests.requests}
          busy={deciding}
          onDecide={(req, verdict, reason) => void onDecide(req, verdict, reason)}
        />
      ) : null}
    </div>
  );
}

const QUIET_BUTTON = "ps-action ps-action--quiet";

// Headline answers the page's question in one sentence, the way the
// findings page opens: how much of your work is in the pipeline, and what
// needs a person. Its clauses open the findings list on exactly those.
function Headline({
  names,
  summary,
  unreadable,
}: {
  names: string[];
  summary: RepositoriesPayload | null;
  unreadable: number;
}) {
  const repos = summary?.repositories ?? [];
  const sum = (phases: Phase[]) =>
    repos.reduce((n, r) => n + phases.reduce((m, p) => m + (r.phases?.[p] ?? 0), 0), 0);
  const inPipeline = sum(IN_PIPELINE);
  const awaiting = sum(["AwaitingApproval"]);
  const failed = sum(["Failed"]);

  const clause = (n: number, text: string, phases: Phase[]) => (
    <a
      class="ps-clause"
      href={findingsHrefFor(names, phases)}
    >
      {formatCount(n)} {text}
    </a>
  );
  const attention: ComponentChildren[] = [];
  if (awaiting) attention.push(clause(awaiting, awaiting === 1 ? "is waiting for approval" : "are waiting for approval", ["AwaitingApproval"]));
  if (failed) attention.push(clause(failed, failed === 1 ? "has failed" : "have failed", ["Failed"]));

  return (
    <section aria-labelledby="repositories-headline">
      <h1 class="ps-title">My repositories</h1>
      {names.length === 0 || !summary ? (
        <p id="repositories-headline" class="mt-2 mb-0 max-w-[60ch] text-[15px] text-muted">
          {names.length === 0
            ? "Keep a list of the repositories you look after, and see where each one's findings stand."
            : "Loading your repositories…"}
        </p>
      ) : (
        <>
          <p
            id="repositories-headline"
            class="mt-3 mb-0 max-w-[52ch] text-[22px] leading-snug font-bold text-balance text-fg max-sm:text-[19px]"
          >
            {inPipeline ? (
              <a class="text-fg no-underline hover:underline" href={findingsHrefFor(names, IN_PIPELINE)}>
                {plural(inPipeline, "finding", "findings")} in progress
              </a>
            ) : (
              "Nothing in progress"
            )}{" "}
            across your {plural(names.length, "repository", "repositories")}.{" "}
            {attention.length === 2 ? (
              <>
                {attention[0]} and {attention[1]}.
              </>
            ) : attention.length === 1 ? (
              <>{attention[0]}.</>
            ) : inPipeline ? (
              <span class="text-muted">Nothing needs a person right now.</span>
            ) : null}
          </p>
          {unreadable ? (
            <p class="mt-2 mb-0 text-[15px] text-muted">
              Patchy can't read the alerts of {unreadable === 1 ? "one of them" : `${formatCount(unreadable)} of them`} yet.
            </p>
          ) : null}
        </>
      )}
    </section>
  );
}

// StationHeader names the stations once, above every row. The five
// pipeline stations share a rail; Failed and Closed stand apart, as the two
// ways out of it.
function StationHeader() {
  const label = (g: PhaseGroup) => (
    <span key={g.id} class="px-1 text-center text-[12px] leading-tight text-muted" title={g.phases.join(", ")}>
      {g.label}
    </span>
  );
  return (
    <div class={`${ROW_GRID} border-b border-line px-5 py-2.5 max-md:hidden`} aria-hidden="true">
      <span />
      {TRACK.map(label)}
      <span />
      {TERMINAL.map(label)}
      <span />
    </div>
  );
}

// Station is one stage of one repository: a tinted disc holding the count
// when there are findings there, a dot on the rail when there are none.
function Station({
  name,
  group,
  n,
  rail,
}: {
  name: string;
  group: PhaseGroup;
  n: number;
  rail: "start" | "mid" | "end" | "none";
}) {
  const line = {
    start: "left-1/2 right-0",
    mid: "inset-x-0",
    end: "left-0 right-1/2",
    none: "hidden",
  }[rail];
  return (
    <div class="relative flex h-9 items-center justify-center">
      <span aria-hidden="true" class={`absolute top-1/2 h-px bg-line-2 ${line}`} />
      {n > 0 ? (
        <a
          href={findingsHref(name, group.phases)}
          class={`relative inline-flex h-7 min-w-7 items-center justify-center rounded-full px-2 text-[13px] font-bold tabular-nums no-underline hover:outline-2 hover:outline-offset-1 hover:outline-current ${STATION[group.tone]}`}
          aria-label={`${plural(n, "finding", "findings")} ${group.label.toLowerCase()} in ${name}`}
        >
          {formatCount(n)}
        </a>
      ) : (
        <span aria-hidden="true" class="relative size-1.5 rounded-full bg-line-3" />
      )}
    </div>
  );
}

function RepositoryRow({
  name,
  summary,
  loaded,
  access,
  accessPending,
  backfill,
  onRemove,
}: {
  name: string;
  summary?: RepositorySummary;
  loaded: boolean;
  access?: RepositoryAccess;
  accessPending: boolean;
  backfill: RowBackfill;
  onRemove: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [showBackfill, setShowBackfill] = useState(false);
  const short = shortName(name);
  const findings = summary?.findings ?? 0;
  const details = access && !access.skipped ? detailLines(name, access) : [];
  const slug = name.replace(/[^A-Za-z0-9_-]/g, "-");
  const detailsId = `why-${slug}`;
  const backfillId = `backfill-${slug}`;
  const bf = summary?.backfill;
  const state = bf ? backfillState(bf, ago) : null;
  // A row on the track has no room for the backfill inline: its name
  // carries a toggle (the request's state, when there is one) that opens
  // the control under the row.
  const toggle = findings > 0 && (backfill.mode !== "none" || (state && state.phase !== "Complete"));

  return (
    <li class={`${ROW_GRID} group px-5 py-2 hover:bg-[color-mix(in_oklab,var(--patchy-green)_4%,var(--patchy-surface))] max-md:py-3`}>
      <div class="flex min-w-0 items-center justify-between gap-2 md:block md:pr-4">
        <div class="min-w-0">
          <a
            class="block truncate text-[14.5px] font-semibold text-fg no-underline hover:underline"
            href={findingsHref(name)}
            title={`Findings in ${name}`}
          >
            {short}
          </a>
          {(summary?.rollup && findings > 0) || toggle ? (
            <span class="flex flex-wrap items-center gap-x-2 text-[12.5px] text-muted">
              {summary?.rollup && findings > 0 ? (
                <a class="text-muted no-underline hover:text-fg hover:underline" href={metricsHref(name)}>
                  {plural(summary.rollup.findings, "finding", "findings")} all-time
                </a>
              ) : null}
              {toggle ? (
                <button
                  type="button"
                  class={`cursor-pointer border-0 bg-transparent p-0 text-[12.5px] underline decoration-line-3 underline-offset-4 hover:text-fg hover:decoration-current ${
                    state && state.phase !== "Complete" ? STATE_TEXT[state.tone] : "text-muted"
                  }`}
                  aria-expanded={showBackfill}
                  aria-controls={backfillId}
                  onClick={() => setShowBackfill((o) => !o)}
                >
                  {state && state.phase !== "Complete" ? state.label : "Backfill"}
                </button>
              ) : null}
            </span>
          ) : null}
        </div>
        <RemoveButton name={name} onRemove={onRemove} class="md:hidden" />
      </div>

      {!loaded ? (
        <p class="m-0 text-[13px] text-faint md:col-span-8">Loading…</p>
      ) : findings > 0 ? (
        <>
          {TRACK.map((g, i) => (
            <div key={g.id} class="max-md:hidden">
              <Station name={name} group={g} n={groupCount(summary, g)} rail={i === 0 ? "start" : i === TRACK.length - 1 ? "end" : "mid"} />
            </div>
          ))}
          <span class="max-md:hidden" />
          {TERMINAL.map((g) => (
            <div key={g.id} class="max-md:hidden">
              <Station name={name} group={g} n={groupCount(summary, g)} rail="none" />
            </div>
          ))}
          <CompactStations name={name} summary={summary} />
        </>
      ) : (
        <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1.5 py-1.5 text-[13.5px] md:col-span-8">
          <QuietStatus
            name={name}
            summary={summary}
            access={access}
            accessPending={accessPending}
            backfill={backfill}
            why={
              details.length ? (
                <button
                  type="button"
                  class="cursor-pointer border-0 bg-transparent p-0 text-[13px] text-muted underline decoration-line-3 underline-offset-4 hover:text-fg hover:decoration-current"
                  aria-expanded={open}
                  aria-controls={detailsId}
                  onClick={() => setOpen((o) => !o)}
                >
                  {open ? "Hide details" : "Why?"}
                </button>
              ) : null
            }
          />
        </div>
      )}

      <RemoveButton name={name} onRemove={onRemove} class="justify-self-end max-md:hidden" />

      {open && details.length ? (
        <ul
          id={detailsId}
          class="m-0 mt-1 mb-2 flex list-none flex-col gap-1.5 rounded-lg bg-surface-2 px-4 py-3 text-[13px] leading-relaxed text-muted md:col-span-8 md:col-start-2 max-md:mt-2"
        >
          {details.map((line) => (
            <li key={line} class="max-w-[80ch]">
              {line}
            </li>
          ))}
        </ul>
      ) : null}

      {toggle && showBackfill ? (
        <div
          id={backfillId}
          class="mt-1 mb-2 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg bg-surface-2 px-4 py-3 text-[13px] text-muted md:col-span-8 md:col-start-2 max-md:mt-2"
        >
          {state ? (
            <>
              {state.phase !== "Complete" ? <Pill tone={state.tone}>{state.label}</Pill> : null}
              <span class="max-w-[72ch]">{state.text}</span>
            </>
          ) : (
            <span class="max-w-[72ch]">Lists the repository's open alerts again and ingests any patchy missed.</span>
          )}
          <span class="ml-auto flex items-center gap-1.5">
            <BackfillAction bf={bf} backfill={backfill} />
          </span>
        </div>
      ) : null}
    </li>
  );
}

// STATE_TEXT tints the toggle naming an unfinished or refused request.
const STATE_TEXT: Record<"amber" | "seedling" | "green" | "red" | "soil", string> = {
  amber: "text-amber",
  seedling: "text-seedling",
  green: "text-ink",
  red: "text-red",
  soil: "text-soil",
};

// detailLines are the full explanations behind a row's short status.
function detailLines(name: string, access: RepositoryAccess): string[] {
  const lines = access.backfillIntegration ? [] : accessProblems(access);
  const forge = forgeProblem(name, access.forge);
  return forge ? [...lines, forge] : lines;
}

// CompactStations is the track on a phone: only the stages holding
// findings, each a tinted chip naming its stage.
function CompactStations({ name, summary }: { name: string; summary?: RepositorySummary }) {
  return (
    <ul class="m-0 mt-2 flex list-none flex-wrap gap-1.5 p-0 md:hidden">
      {PHASE_GROUPS.map((g) => {
        const n = groupCount(summary, g);
        return n ? (
          <li key={g.id}>
            <a
              href={findingsHref(name, g.phases)}
              class={`inline-flex h-7 items-center gap-1.5 rounded-full px-2.5 text-[12.5px] font-semibold no-underline ${STATION[g.tone]}`}
            >
              <span class="tabular-nums">{formatCount(n)}</span> {g.label.toLowerCase()}
            </a>
          </li>
        ) : null;
      })}
    </ul>
  );
}

function RemoveButton({ name, onRemove, class: extra }: { name: string; onRemove: () => void; class: string }) {
  return (
    <button
      type="button"
      class={`grid size-8 flex-none cursor-pointer place-items-center rounded-md border-0 bg-transparent text-faint hover:bg-surface-2 hover:text-fg focus-visible:opacity-100 md:opacity-0 md:group-hover:opacity-100 ${extra}`}
      aria-label={`Stop tracking ${name}`}
      title="Stop tracking"
      onClick={onRemove}
    >
      <Icon name="x" size={15} />
    </button>
  );
}

// BackfillAction is a row's backfill control: nothing while a request is
// open (its state says so), the button the caller's grants allow, held
// disabled until the cooldown passes.
function BackfillAction({ bf, backfill }: { bf?: RepositoryBackfill; backfill: RowBackfill }) {
  if (backfillOpen(bf)) return null;
  if (backfill.mode === "none") return <span class="text-[13px] text-faint">Ask an operator to backfill it.</span>;
  const again = !!bf;
  const wait = !backfill.available ? (
    <span class="text-[13px] text-faint" title={backfill.availableAt}>
      available {formatIn(backfill.availableAt)}
    </span>
  ) : null;
  if (backfill.mode === "request") {
    return (
      <>
        {/* A row with history already has its sentence; the hint would
            only push the button onto a line of its own. */}
        {wait ?? (again ? null : <span class="text-[13px] text-faint">An operator will review it.</span>)}
        <ConfirmButton
          icon="rotateCcw"
          label={again ? "Request again" : "Request backfill"}
          confirmLabel="Confirm request"
          busyLabel="Requesting…"
          busy={backfill.busy}
          disabled={!backfill.available}
          onConfirm={() => backfill.onBackfill(true)}
        />
      </>
    );
  }
  return (
    <>
      {wait}
      <BackfillButton
        label={again ? "Backfill again" : "Backfill"}
        busy={backfill.busy}
        disabled={!backfill.available}
        onConfirm={() => backfill.onBackfill(false)}
      />
    </>
  );
}

// QuietStatus is the sentence a repository with no current findings gets
// in place of the track: an unfinished or refused request first, then its
// history when patchy has one, otherwise what patchy can do about it.
function QuietStatus({
  name,
  summary,
  access,
  accessPending,
  backfill,
  why,
}: {
  name: string;
  summary?: RepositorySummary;
  access?: RepositoryAccess;
  accessPending: boolean;
  backfill: RowBackfill;
  // why is the toggle for the row's full explanation, placed right after
  // the short notes it expands.
  why: ComponentChildren;
}) {
  const bf = summary?.backfill;
  const state = bf ? backfillState(bf, ago) : null;
  const unreadable = !!access && !access.skipped && !access.backfillIntegration;

  const forge = access && !access.skipped ? forgeBadge(access.forge) : null;
  const forgeNote = forge ? (
    <span class="inline-flex items-center gap-1.5 text-[13px] text-muted" title={forgeProblem(name, access?.forge) ?? ""}>
      <Icon name="alertTriangle" size={13} class="text-amber" />
      {forge}
    </span>
  ) : null;
  const action = (
    <span class="ml-auto flex items-center gap-1.5">
      <BackfillAction bf={bf} backfill={backfill} />
    </span>
  );

  if (state && state.phase !== "Complete") {
    return (
      <>
        <Pill tone={state.tone}>{state.label}</Pill>
        <span class="min-w-0 text-muted" title={state.phase === "Failed" ? state.text : undefined}>
          {state.text}
        </span>
        {forgeNote}
        {why}
        {unreadable ? null : action}
      </>
    );
  }

  if (summary?.rollup) {
    return (
      <>
        <span class="text-muted">
          Nothing open now.{" "}
          <a class="text-muted underline decoration-line-3 underline-offset-4 hover:text-fg" href={metricsHref(name)}>
            {plural(summary.rollup.findings, "finding", "findings")} all-time
          </a>
          {summary.rollup.lastProcessed ? `, the latest ${ago(summary.rollup.lastProcessed)}.` : "."}
        </span>
        {action}
      </>
    );
  }

  if (accessPending) return <span class="text-faint">Checking whether patchy can read its alerts…</span>;

  if (unreadable) {
    const reason = accessReason(access!);
    return (
      <>
        <Pill tone="amber">Can't read alerts</Pill>
        <span class="text-muted">{reason.charAt(0).toUpperCase() + reason.slice(1)}.</span>
        {forgeNote}
        {why}
      </>
    );
  }

  return (
    <>
      <span class="text-muted">
        {state
          ? state.text
          : access?.backfillIntegration
            ? `No findings yet. ${access.backfillIntegration} can read its alerts.`
            : "No findings yet."}
      </span>
      {forgeNote}
      {why}
      {action}
    </>
  );
}

// GitHubBar is the GitHub link: each Integration's connect button, or the
// account connected through it, then the import and refresh of the list.
function GitHubBar({
  links,
  canBackfillMaintained,
  busy,
  importOpen,
  onConnect,
  onDisconnect,
  onToggleImport,
  onRefresh,
}: {
  links: GitHubLink[];
  canBackfillMaintained: boolean;
  // The Integration being connected or disconnected, or "refresh".
  busy: string | null;
  importOpen: boolean;
  onConnect: (integration: string) => void;
  onDisconnect: (integration: string) => void;
  onToggleImport: () => void;
  onRefresh: () => void;
}) {
  const connected = anyConnected(links);
  const all = links.every((l) => l.connected);
  return (
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2 text-[13.5px]" role="group" aria-label="GitHub">
      <Icon name="github" size={16} class="flex-none text-muted" />
      {!connected ? (
        <span class="text-muted">
          Connect GitHub to import the repositories you maintain
          {canBackfillMaintained ? " and backfill them yourself" : ""}.
        </span>
      ) : null}
      {links.map((l) =>
        l.connected ? (
          <span key={l.integration} class="inline-flex flex-wrap items-center gap-x-1.5">
            <span class="text-muted">Connected to {l.host} as</span>
            <a class="font-semibold text-fg no-underline hover:underline" href={githubProfileHref(l)} target="_blank" rel="noreferrer">
              @{l.login}
            </a>
            {links.length > 1 ? <span class="text-faint">({l.integration})</span> : null}
            <span class="text-faint" aria-hidden="true">
              ·
            </span>
            <button
              type="button"
              class="cursor-pointer border-0 bg-transparent p-0 text-[13.5px] text-muted underline decoration-line-3 underline-offset-4 hover:text-fg hover:decoration-current disabled:cursor-default disabled:opacity-60"
              disabled={busy === l.integration}
              onClick={() => onDisconnect(l.integration)}
            >
              {busy === l.integration ? "Disconnecting…" : "Disconnect"}
            </button>
          </span>
        ) : (
          <a
            key={l.integration}
            class={`ps-action no-underline ${busy === l.integration ? "pointer-events-none opacity-60" : ""}`}
            href={githubConnectHref(l.integration, githubBack())}
            onClick={(e) => {
              e.preventDefault();
              onConnect(l.integration);
            }}
          >
            {busy === l.integration ? "Connecting…" : `Connect GitHub — ${l.integration} (${l.host})`}
          </a>
        ),
      )}
      {connected ? (
        <span class="ml-auto flex items-center gap-1">
          <button
            type="button"
            class={QUIET_BUTTON}
            disabled={busy === "refresh" || !all}
            title={
              all
                ? "Remove the repositories you imported from GitHub and no longer maintain"
                : "Connect every GitHub account first, so the refresh sees all you maintain"
            }
            onClick={onRefresh}
          >
            {busy === "refresh" ? "Refreshing…" : "Refresh from GitHub"}
          </button>
          <button type="button" class="ps-action" aria-expanded={importOpen} onClick={onToggleImport}>
            <Icon name="download" size={14} /> Import from GitHub
          </button>
        </span>
      ) : null}
    </div>
  );
}

// GitHubImport is the import picker: every repository the connected
// accounts administer or maintain, filterable, each a checkbox; those
// already on the list are shown ticked and fixed.
function GitHubImport({
  links,
  tracked,
  onAdd,
  onClose,
}: {
  links: GitHubLink[];
  tracked: TrackedRepo[];
  onAdd: (names: string[]) => void;
  onClose: () => void;
}) {
  const [results, setResults] = useState<GitHubFetch[] | null>(null);
  const [term, setTerm] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const linksKey = links.map((l) => `${l.integration}:${l.connected}`).join(",");
  useEffect(() => {
    const ctl = new AbortController();
    setResults(null);
    void fetchAllGitHub(links, ctl.signal).then((r) => {
      if (!ctl.signal.aborted) setResults(r);
    });
    return () => ctl.abort();
    // links is captured through linksKey.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [linksKey]);

  // One list across Integrations, by name, deduplicated.
  const repos = useMemo(() => {
    const out = new Map<string, GitHubRepository>();
    for (const r of results ?? []) {
      if (!r.ok) continue;
      for (const repo of r.payload.repositories) {
        const key = repo.name.toLowerCase();
        if (!out.has(key)) out.set(key, repo);
      }
    }
    return [...out.values()].sort((a, b) => a.name.localeCompare(b.name));
  }, [results]);
  const shown = filterImportable(repos, term);
  const left = room(tracked) - selected.size;
  const fresh = shown.filter((r) => !tracks(tracked, r.name));
  const addAll = fresh.slice(0, room(tracked)).map((r) => r.name);
  const logins = (results ?? []).flatMap((r) => (r.ok ? [`@${r.payload.login}`] : []));
  const failures = (results ?? []).filter((r): r is Extract<GitHubFetch, { ok: false }> => !r.ok);
  const truncated = (results ?? []).some((r) => r.ok && r.payload.truncated);

  const toggle = (name: string) =>
    setSelected((s) => {
      const next = new Set(s);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  const commit = (names: string[]) => {
    onAdd(names);
    setSelected(new Set());
  };

  return (
    <section class="ps-card" aria-labelledby="github-import-title">
      <header class="flex items-start justify-between gap-3 border-b border-line px-5 pt-4 pb-3">
        <div>
          <h2 id="github-import-title" class="ps-heading">
            Import from GitHub
          </h2>
          <p class="m-0 mt-0.5 text-[13px] text-muted">
            {results === null
              ? "Loading the repositories you maintain…"
              : `${plural(repos.length, "repository", "repositories")} ${logins.join(" and ") || "you"} ${
                  logins.length > 1 ? "administer or maintain" : "administers or maintains"
                }. Your list has room for ${formatCount(room(tracked))} more.`}
          </p>
        </div>
        <button
          type="button"
          class="grid size-8 flex-none cursor-pointer place-items-center rounded-md border-0 bg-transparent text-faint hover:bg-surface-2 hover:text-fg"
          aria-label="Close the import"
          onClick={onClose}
        >
          <Icon name="x" size={15} />
        </button>
      </header>

      {failures.map((f) => (
        <p key={f.integration} class="m-0 flex items-start gap-2 border-b border-line px-5 py-2.5 text-[13px] text-red">
          <Icon name="alertTriangle" size={14} class="mt-0.5 flex-none" />
          <span>
            {f.integration}: {f.error}
            {/reconnect/i.test(f.error) ? (
              <>
                {" "}
                <a class="font-semibold text-inherit" href={githubConnectHref(f.integration, githubBack())}>
                  Reconnect
                </a>
              </>
            ) : null}
          </span>
        </p>
      ))}
      {truncated ? (
        <p class="m-0 border-b border-line px-5 py-2.5 text-[13px] text-amber">
          GitHub's list was cut short, so some repositories you maintain may be missing. Add those by name.
        </p>
      ) : null}

      {repos.length ? (
        <>
          <div class="px-5 pt-3 pb-2">
            <input
              type="search"
              class="ps-input w-full text-[13.5px]"
              placeholder="Filter by name"
              aria-label="Filter the repositories you maintain"
              value={term}
              onInput={(e) => setTerm((e.target as HTMLInputElement).value)}
            />
          </div>
          <ul class="m-0 max-h-[22rem] list-none overflow-y-auto p-0 pb-2" aria-label="Repositories you maintain">
            {shown.map((r) => {
              const listed = tracks(tracked, r.name);
              const checked = listed || selected.has(r.name);
              const full = !checked && left <= 0;
              return (
                <li key={r.name}>
                  <label
                    class={`flex items-center gap-3 px-5 py-1.5 text-[13.5px] ${
                      listed || full ? "cursor-default" : "cursor-pointer hover:bg-surface-2"
                    }`}
                  >
                    <input
                      type="checkbox"
                      class="size-4 flex-none accent-turf"
                      checked={checked}
                      disabled={listed || full}
                      onChange={() => toggle(r.name)}
                    />
                    <span class="min-w-0 truncate">
                      <span class="text-faint">{ownerName(r.name)}/</span>
                      <span class={listed ? "text-muted" : "font-semibold text-fg"}>{shortName(r.name)}</span>
                    </span>
                    <span class="ml-auto flex flex-none items-center gap-2 text-[12.5px] text-faint">
                      {listed ? "on your list" : null}
                      <span class="ps-mono-tag">{r.role}</span>
                    </span>
                  </label>
                </li>
              );
            })}
            {shown.length === 0 ? (
              <li class="px-5 py-3 text-[13px] text-faint">No repository you maintain matches “{term}”.</li>
            ) : null}
          </ul>
        </>
      ) : results !== null && failures.length === 0 ? (
        <p class="m-0 px-5 py-4 text-[13.5px] text-muted">
          GitHub lists no repository you administer or maintain where patchy's App is installed.
        </p>
      ) : null}

      <footer class="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-line px-5 py-3 text-[13px] text-muted">
        <span>
          {room(tracked) === 0
            ? `Your list is full at ${MAX_TRACKED} repositories. Remove one to add another.`
            : `${formatCount(selected.size)} selected`}
        </span>
        <span class="ml-auto flex items-center gap-1.5">
          <button
            type="button"
            class={QUIET_BUTTON}
            disabled={addAll.length === 0}
            title={`Adds every repository shown that is not on your list yet, up to the limit of ${MAX_TRACKED}`}
            onClick={() => commit(addAll)}
          >
            {addAll.length < fresh.length ? `Add the first ${formatCount(addAll.length)}` : `Add all ${formatCount(addAll.length)}`}
          </button>
          <button
            type="button"
            class="ps-action ps-action--primary"
            disabled={selected.size === 0}
            onClick={() => commit([...selected])}
          >
            {selected.size ? `Add ${formatCount(selected.size)} selected` : "Add selected"}
          </button>
        </span>
      </footer>
    </section>
  );
}

// BackfillRequests is the operators' queue: every request awaiting a
// decision, each with Approve and Deny (which takes an optional reason).
function BackfillRequests({
  requests,
  busy,
  onDecide,
}: {
  requests: BackfillRequest[];
  busy: string | null;
  onDecide: (req: BackfillRequest, verdict: "approve" | "deny", reason: string) => void;
}) {
  const waiting = requests.filter((r) => r.phase === "AwaitingApproval");
  return (
    <section class="ps-card" aria-labelledby="backfill-requests-title">
      <header class="px-5 pt-4 pb-3">
        <h2 id="backfill-requests-title" class="ps-heading">
          Backfill requests
        </h2>
        <p class="m-0 mt-0.5 text-[13px] text-muted">
          {waiting.length
            ? `${plural(waiting.length, "request is", "requests are")} waiting for you to decide. An approved request runs at once; a denied one tells the requester why.`
            : "Nothing is waiting for a decision."}
        </p>
      </header>
      {waiting.length ? (
        <ul class="m-0 list-none p-0">
          {waiting.map((r) => (
            <BackfillRequestRow key={r.name} request={r} busy={busy === r.name} onDecide={onDecide} />
          ))}
        </ul>
      ) : null}
    </section>
  );
}

function BackfillRequestRow({
  request: r,
  busy,
  onDecide,
}: {
  request: BackfillRequest;
  busy: boolean;
  onDecide: (req: BackfillRequest, verdict: "approve" | "deny", reason: string) => void;
}) {
  const [armed, setArmed] = useState<"approve" | "deny" | null>(null);
  const [reason, setReason] = useState("");
  const what = r.repositories?.length ? r.repositories.join(", ") : `Everything ${r.integration} can see`;
  return (
    <li class="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-line px-5 py-3">
      <div class="min-w-0">
        <p class="m-0 truncate text-[14px] font-semibold text-fg" title={what}>
          {what}
        </p>
        <p class="m-0 text-[13px] text-muted">
          Requested by {r.requestedBy || "someone"}
          {r.requestedAt ? ` ${ago(r.requestedAt)}` : ""} on {r.integration}
          {r.githubLogin ? ` · GitHub @${r.githubLogin}` : ""}
        </p>
      </div>
      <span class="ml-auto flex flex-wrap items-center justify-end gap-1.5">
        {armed === "deny" ? (
          <input
            type="text"
            class="ps-input w-[min(18rem,60vw)] text-[13px]"
            placeholder="Reason (optional)"
            aria-label={`Why the backfill of ${what} is denied`}
            maxLength={500}
            value={reason}
            onInput={(e) => setReason((e.target as HTMLInputElement).value)}
          />
        ) : null}
        <ConfirmButton
          icon="x"
          label="Deny"
          confirmLabel="Confirm deny"
          busyLabel="Deciding…"
          busy={busy}
          armed={armed === "deny"}
          onArmedChange={(a) => setArmed(a ? "deny" : null)}
          onConfirm={() => onDecide(r, "deny", reason.trim())}
        />
        <ConfirmButton
          icon="check"
          label="Approve"
          confirmLabel="Confirm: run it"
          busyLabel="Deciding…"
          primary
          busy={busy}
          armed={armed === "approve"}
          onArmedChange={(a) => setArmed(a ? "approve" : null)}
          onConfirm={() => onDecide(r, "approve", "")}
        />
      </span>
    </li>
  );
}

// AddRepository is the add box: the type-ahead over repositories patchy
// knows (current findings and all-time rollups), plus the exact name typed,
// for a repository patchy has never seen.
function AddRepository({ tracked, onAdd, full }: { tracked: string[]; onAdd: (name: string) => void; full: boolean }) {
  const [term, setTerm] = useState("");
  const [remote, setRemote] = useState<{ term: string; names: Map<string, string> } | null>(null);

  useEffect(() => {
    const ctl = new AbortController();
    const timer = setTimeout(() => {
      Promise.all([
        suggestFindings({ q: term, limit: SUGGESTIONS }, ctl.signal).catch(() => null),
        term ? queryRepositories({ q: term, limit: SUGGESTIONS }, ctl.signal).catch(() => null) : null,
      ]).then(([findings, rollups]) => {
        if (ctl.signal.aborted) return;
        // name → the figure shown beside it.
        const names = new Map<string, string>();
        for (const r of findings?.repositories ?? []) names.set(r.name, plural(r.findings, "finding", "findings"));
        for (const r of rollups?.repositories ?? []) {
          const key = r.scope.key ?? "";
          if (key && !names.has(key)) names.set(key, `${formatCount(r.bucket.findings ?? 0)} all-time`);
        }
        setRemote({ term, names });
      });
    }, DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      ctl.abort();
    };
  }, [term]);

  const trackedKey = tracked.join("\n");
  const suggest = useCallback(
    (t: string): FilterGroup[] => {
      if (full) {
        return [{ note: `Your list is full at ${MAX_TRACKED} repositories. Remove one to add another.`, options: [] }];
      }
      const needle = t.toLowerCase();
      const known = [...(remote?.names.keys() ?? [])].filter((n) => !tracked.includes(n));
      const [ranked] = rank(
        known,
        known.map((n) => n.toLowerCase()),
        needle,
        SUGGESTIONS,
      );
      const options: FilterOption[] = ranked.map(({ item, at }) => ({
        key: item,
        content: <Highlight text={item} at={at} len={t.length} />,
        aside: remote?.names.get(item),
        onPick: () => onAdd(item),
      }));
      const groups: FilterGroup[] = [{ label: "Repositories patchy knows", options }];
      const exact = normalizeRepoName(t);
      if (exact && !tracked.includes(exact) && !known.includes(exact)) {
        groups.unshift({
          options: [
            {
              key: `add:${exact}`,
              content: (
                <span>
                  Add <span class="font-semibold">{exact}</span>
                </span>
              ),
              aside: "not seen by patchy yet",
              onPick: () => onAdd(exact),
            },
          ],
        });
      }
      return groups;
    },
    // tracked is captured through trackedKey.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [remote, trackedKey, full, onAdd],
  );

  return (
    <FilterBox
      chips={[]}
      onRemove={() => {}}
      suggest={suggest}
      onQuery={setTerm}
      label="Add a repository"
      placeholder="Add a repository by owner/name"
      empty={(t) =>
        t
          ? "Type the full owner/name, or paste its GitHub URL, to add a repository patchy hasn't seen yet."
          : "Type a repository name."
      }
    />
  );
}
