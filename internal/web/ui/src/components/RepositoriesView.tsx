// The repositories page: the repositories this browser tracks, each drawn
// as its findings' position on patchy's pipeline (every count opens the
// findings list filtered to it). A repository with nothing in the pipeline
// gets one sentence instead: its history, or, when patchy has never seen it,
// whether patchy can read its alerts, with a one-repository backfill. The
// list lives in localStorage (trackedRepos.ts); the server never learns it
// beyond the names a request carries.

import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import {
  AuthRequiredError,
  ForbiddenError,
  fetchRepositories,
  fetchRepositoryAccess,
  postRepositoryBackfill,
  queryRepositories,
  subscribe,
  subscribeConfig,
  suggestFindings,
} from "../api";
import type { Persona } from "../mock/personas";
import { DEMO_TRACKED } from "../mock/repositories";
import {
  PHASE_GROUPS,
  accessProblems,
  accessReason,
  findingsHref,
  findingsHrefFor,
  forgeBadge,
  forgeProblem,
  groupCount,
  metricsHref,
  neverSeen,
  ownerGroups,
  type GroupTone,
  type PhaseGroup,
} from "../repositories";
import {
  MAX_TRACKED,
  STORAGE_KEY,
  add,
  defaultStore,
  exportJSON,
  importJSON,
  normalizeRepoName,
  remove,
  save,
  useTrackedRepos,
  type KeyValueStore,
} from "../trackedRepos";
import type { Phase, RepositoriesPayload, RepositoryBackfill, RepositoryAccess, RepositoryAccessPayload, RepositorySummary } from "../types";
import { formatAgo, formatCount } from "../format";
import { rank } from "../typeahead";
import { FilterBox, Highlight, type FilterGroup, type FilterOption } from "./FilterBox";
import { Icon } from "./icons";
import { Pill } from "./Pills";

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

function plural(n: number, one: string, many: string) {
  return `${formatCount(n)} ${n === 1 ? one : many}`;
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
  // Counts move as findings move: refetch on the findings-changed signal.
  const loadRef = useRef(load);
  loadRef.current = load;
  useEffect(() => subscribe(() => void loadRef.current()), []);
  // A backfill's progress lives on its Integration, not on any finding: a
  // walk that ingests nothing changes no finding, so listen for that too.
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

  const [busy, setBusy] = useState<string | null>(null);
  const onBackfill = useCallback(
    async (repo: string) => {
      setBusy(repo);
      try {
        const res = await postRepositoryBackfill(repo, persona);
        onToast(
          res.outcome === "covered"
            ? `A backfill already waiting on ${res.integration} covers ${repo}.`
            : `Backfill of ${repo} requested on ${res.integration}. Its open alerts arrive as findings shortly.`,
          "green",
        );
        await load();
        // Demo mode has no change stream, and its stand-in controller
        // finishes the request a moment later: look once more then.
        if (demo) setTimeout(() => void loadRef.current(), 2000);
      } catch (e) {
        if (e instanceof AuthRequiredError) onAuthError(e);
        else onToast(e instanceof Error ? e.message : String(e), "red");
      } finally {
        setBusy(null);
      }
    },
    [demo, load, onAuthError, onToast, persona],
  );

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

  return (
    <div class="flex flex-col gap-5">
      <Headline names={names} summary={summary} unreadable={unreadable} />

      <AddRepository tracked={names} onAdd={addRepo} full={tracked.repos.length >= MAX_TRACKED} />

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

      <section class="rounded-[10px] border border-line bg-surface" aria-label="Tracked repositories">
        {tracked.repos.length === 0 ? (
          <div class="px-6 py-12 text-center">
            <p class="m-0 text-[15px] font-semibold text-fg">No repositories yet</p>
            <p class="mx-auto mt-1.5 mb-0 max-w-[52ch] text-[13.5px] text-muted">
              Add the repositories you look after to see where their findings stand, or import a list you exported
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
                      canBackfill={!!summary?.canBackfill}
                      busy={busy === name}
                      onBackfill={() => void onBackfill(name)}
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
    </div>
  );
}

const QUIET_BUTTON =
  "inline-flex min-h-8 cursor-pointer items-center gap-1.5 rounded-md border-0 bg-transparent px-2.5 text-[13px] font-semibold text-muted hover:bg-surface-2 hover:text-fg disabled:cursor-default disabled:opacity-50 disabled:hover:bg-transparent";

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
      class="text-amber underline decoration-2 underline-offset-4 hover:decoration-[3px]"
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
      <h1 class="m-0 text-[28px] leading-tight font-bold tracking-[-0.015em]">My repositories</h1>
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
  canBackfill,
  busy,
  onBackfill,
  onRemove,
}: {
  name: string;
  summary?: RepositorySummary;
  loaded: boolean;
  access?: RepositoryAccess;
  accessPending: boolean;
  canBackfill: boolean;
  busy: boolean;
  onBackfill: () => void;
  onRemove: () => void;
}) {
  const [open, setOpen] = useState(false);
  const short = name.slice(name.indexOf("/") + 1);
  const findings = summary?.findings ?? 0;
  const details = access && !access.skipped ? detailLines(name, access) : [];
  const detailsId = `why-${name.replace(/[^A-Za-z0-9_-]/g, "-")}`;

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
          {summary?.rollup && findings > 0 ? (
            <a class="text-[12.5px] text-muted no-underline hover:text-fg hover:underline" href={metricsHref(name)}>
              {plural(summary.rollup.findings, "finding", "findings")} all-time
            </a>
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
            canBackfill={canBackfill}
            busy={busy}
            onBackfill={onBackfill}
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
    </li>
  );
}

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

// backfilledSentence reports a finished backfill that found nothing: the
// repository had no open alerts when it was walked.
function backfilledSentence(bf: RepositoryBackfill): string {
  const when = bf.lastRunAt ? ` ${ago(bf.lastRunAt)}` : "";
  return `Backfilled on ${bf.integration}${when}. It had no open alerts.`;
}

// QuietStatus is the sentence a repository with no current findings gets
// in place of the track: its history when patchy has one, otherwise what
// patchy can do about it.
function QuietStatus({
  name,
  summary,
  access,
  accessPending,
  canBackfill,
  busy,
  onBackfill,
  why,
}: {
  name: string;
  summary?: RepositorySummary;
  access?: RepositoryAccess;
  accessPending: boolean;
  canBackfill: boolean;
  busy: boolean;
  onBackfill: () => void;
  // why is the toggle for the row's full explanation, placed right after
  // the short notes it expands.
  why: ComponentChildren;
}) {
  const [confirming, setConfirming] = useState(false);

  if (summary?.rollup) {
    return (
      <span class="text-muted">
        Nothing open now.{" "}
        <a class="text-muted underline decoration-line-3 underline-offset-4 hover:text-fg" href={metricsHref(name)}>
          {plural(summary.rollup.findings, "finding", "findings")} all-time
        </a>
        {summary.rollup.lastProcessed ? `, the latest ${ago(summary.rollup.lastProcessed)}.` : "."}
      </span>
    );
  }

  const bf = summary?.backfill;
  if (bf?.pending) {
    return (
      <>
        <Pill tone="seedling">Backfill pending</Pill>
        <span class="text-muted">
          Requested on {bf.integration}
          {bf.requestedAt ? ` ${ago(bf.requestedAt)}` : ""}
          {bf.requestedBy ? ` by ${bf.requestedBy}` : ""}. Its alerts arrive once the controller picks it up.
        </span>
      </>
    );
  }
  if (bf?.error) {
    return (
      <>
        <Pill tone="red">Backfill failed</Pill>
        <span class="min-w-0 truncate text-muted" title={bf.error}>
          {bf.error}
        </span>
      </>
    );
  }
  if (accessPending) return <span class="text-faint">Checking whether patchy can read its alerts…</span>;
  if (!access || access.skipped) {
    return <span class="text-muted">{bf ? backfilledSentence(bf) : "No findings yet."}</span>;
  }

  const forge = forgeBadge(access.forge);
  const forgeNote = forge ? (
    <span class="inline-flex items-center gap-1.5 text-[13px] text-muted" title={forgeProblem(name, access.forge) ?? ""}>
      <Icon name="alertTriangle" size={13} class="text-amber" />
      {forge}
    </span>
  ) : null;

  if (!access.backfillIntegration) {
    const reason = accessReason(access);
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
        {bf ? backfilledSentence(bf) : `No findings yet. ${access.backfillIntegration} can read its alerts.`}
      </span>
      {forgeNote}
      {why}
      <span class="ml-auto flex items-center gap-1.5">
        {canBackfill ? (
          <>
            {confirming ? (
              <button type="button" class={QUIET_BUTTON} onClick={() => setConfirming(false)}>
                Cancel
              </button>
            ) : null}
            <button
              type="button"
              class={`ps-action min-h-8 px-3 text-[13px] ${confirming ? "is-confirming" : ""}`}
              disabled={busy}
              onClick={() => {
                if (confirming) {
                  setConfirming(false);
                  onBackfill();
                } else {
                  setConfirming(true);
                }
              }}
            >
              <Icon name="rotateCcw" size={14} />
              {busy ? "Requesting…" : confirming ? "Confirm backfill" : bf ? "Backfill again" : "Backfill"}
            </button>
          </>
        ) : (
          <span class="text-[13px] text-faint">Ask an operator to backfill it.</span>
        )}
      </span>
    </>
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
