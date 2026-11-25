// The repositories page's list: the repositories one maintainer tracks,
// kept in this browser's localStorage. Nothing about it reaches the cluster;
// export and import move it between browsers.
//
// Every entry records where it came from (`source`). v1 has only manual
// entries; a later "Connect GitHub" discovery source would add entries with
// source "github" without changing the stored shape.
//
// Storage can be missing or throw (private windows, blocked site data), so
// every access is guarded: the page then works for the visit and says the
// list will not be kept.

import { useCallback, useState } from "preact/hooks";

export const STORAGE_KEY = "patchy-tracked-repos";
// MAX_TRACKED matches the server's cap on GET /api/repositories.
export const MAX_TRACKED = 50;

export type TrackedSource = "manual" | "github";

export interface TrackedRepo {
  name: string; // exact "owner/name"
  addedAt: string; // ISO time
  source?: TrackedSource; // absent means manual
}

interface StoredList {
  version: 1;
  repos: TrackedRepo[];
}

// KeyValueStore is the slice of Storage used here, so tests can pass a fake.
export interface KeyValueStore {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

// REPO_NAME mirrors internal/repoaccess.ValidRepository: GitHub owners are
// alphanumerics and hyphens, names add dots and underscores.
const REPO_NAME = /^[A-Za-z0-9][A-Za-z0-9-]*\/[A-Za-z0-9._-]+$/;

export function validRepoName(name: string): boolean {
  return name.length <= 200 && REPO_NAME.test(name) && !/\/\.\.?$/.test(name);
}

// normalizeRepoName accepts the forms people paste: "owner/name", a GitHub
// URL, or either with a ".git" suffix. Returns null when it is none of
// those.
export function normalizeRepoName(input: string): string | null {
  let s = input.trim();
  const url = /^(?:https?:\/\/)?(?:www\.)?github\.com\/([^/\s]+\/[^/\s?#]+)/i.exec(s);
  if (url) s = url[1];
  s = s.replace(/\.git$/, "").replace(/\/+$/, "");
  return validRepoName(s) ? s : null;
}

export function defaultStore(): KeyValueStore | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

function sanitize(raw: unknown): TrackedRepo[] {
  const list = Array.isArray(raw) ? raw : [];
  const out: TrackedRepo[] = [];
  for (const r of list) {
    if (!r || typeof r !== "object") continue;
    const { name, addedAt, source } = r as Record<string, unknown>;
    if (typeof name !== "string" || !validRepoName(name) || out.some((x) => x.name === name)) continue;
    out.push({
      name,
      addedAt: typeof addedAt === "string" ? addedAt : new Date(0).toISOString(),
      ...(source === "github" ? { source: "github" as const } : {}),
    });
    if (out.length >= MAX_TRACKED) break;
  }
  return out;
}

// load reads the stored list; anything unreadable is an empty list.
export function load(store: KeyValueStore | null = defaultStore()): TrackedRepo[] {
  if (!store) return [];
  try {
    const raw = store.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as Partial<StoredList>;
    return parsed?.version === 1 ? sanitize(parsed.repos) : [];
  } catch {
    return [];
  }
}

// save writes the list, reporting whether it stuck.
export function save(repos: TrackedRepo[], store: KeyValueStore | null = defaultStore()): boolean {
  if (!store) return false;
  try {
    const list: StoredList = { version: 1, repos };
    store.setItem(STORAGE_KEY, JSON.stringify(list));
    return true;
  } catch {
    return false;
  }
}

export type AddResult = { repos: TrackedRepo[]; error?: string };

// add appends one repository, refusing duplicates, bad names and a full list.
export function add(repos: TrackedRepo[], input: string, now = new Date()): AddResult {
  const name = normalizeRepoName(input);
  if (!name) return { repos, error: `“${input.trim()}” is not an owner/name repository.` };
  if (repos.some((r) => r.name === name)) return { repos, error: `${name} is already on your list.` };
  if (repos.length >= MAX_TRACKED) return { repos, error: `Your list is full (${MAX_TRACKED} repositories).` };
  return { repos: [...repos, { name, addedAt: now.toISOString() }] };
}

export function remove(repos: TrackedRepo[], name: string): TrackedRepo[] {
  return repos.filter((r) => r.name !== name);
}

export function exportJSON(repos: TrackedRepo[]): string {
  const list: StoredList = { version: 1, repos };
  return JSON.stringify(list, null, 2) + "\n";
}

export interface ImportResult {
  repos: TrackedRepo[];
  added: number;
  skipped: number; // invalid, duplicate, or past the cap
}

// importJSON merges an exported list (or a bare array of names) into
// repos. Throws on text that is not JSON.
export function importJSON(text: string, repos: TrackedRepo[], now = new Date()): ImportResult {
  const parsed = JSON.parse(text) as unknown;
  const entries: unknown[] = Array.isArray(parsed)
    ? parsed
    : parsed && typeof parsed === "object" && Array.isArray((parsed as StoredList).repos)
      ? (parsed as StoredList).repos
      : [];
  let out = repos;
  let added = 0;
  let skipped = 0;
  for (const e of entries) {
    const name = typeof e === "string" ? e : e && typeof e === "object" ? (e as TrackedRepo).name : undefined;
    const res = typeof name === "string" ? add(out, name, now) : { repos: out, error: "invalid" };
    if (res.error) {
      skipped++;
    } else {
      out = res.repos;
      added++;
    }
  }
  return { repos: out, added, skipped };
}

// useTrackedRepos is the page's list, persisted on every change. persisted
// is false when storage is unavailable, so the page can say the list lasts
// only for this visit.
export function useTrackedRepos(store: KeyValueStore | null = defaultStore()) {
  const [repos, setRepos] = useState<TrackedRepo[]>(() => load(store));
  const [persisted, setPersisted] = useState(() => store !== null);
  const update = useCallback(
    (next: TrackedRepo[]) => {
      setRepos(next);
      setPersisted(save(next, store));
    },
    [store],
  );
  return { repos, persisted, update };
}
