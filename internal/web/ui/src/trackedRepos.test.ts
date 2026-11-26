import { describe, expect, test } from "bun:test";
import {
  MAX_TRACKED,
  STORAGE_KEY,
  add,
  exportJSON,
  importGitHub,
  importJSON,
  load,
  normalizeRepoName,
  refreshGitHub,
  remove,
  room,
  save,
  tracks,
  type KeyValueStore,
  type TrackedRepo,
} from "./trackedRepos";

class MemoryStore implements KeyValueStore {
  data = new Map<string, string>();
  getItem(key: string): string | null {
    return this.data.get(key) ?? null;
  }
  setItem(key: string, value: string): void {
    this.data.set(key, value);
  }
}

// ThrowingStore is a private window or blocked site data: every access
// throws.
class ThrowingStore implements KeyValueStore {
  getItem(): string | null {
    throw new DOMException("denied", "SecurityError");
  }
  setItem(): void {
    throw new DOMException("quota", "QuotaExceededError");
  }
}

const now = new Date("2026-11-25T10:00:00Z");

describe("normalizeRepoName", () => {
  test.each([
    ["acme/shop", "acme/shop"],
    ["  acme/shop  ", "acme/shop"],
    ["https://github.com/acme/shop", "acme/shop"],
    ["https://github.com/acme/shop.git", "acme/shop"],
    ["github.com/acme/shop/pulls", "acme/shop"],
    ["acme/shop.web_v2", "acme/shop.web_v2"],
    ["acme/", null],
    ["acme", null],
    ["acme/shop/extra", null],
    ["acme /shop", null],
    ["acme/..", null],
  ])("%p → %p", (input, want) => {
    expect(normalizeRepoName(input)).toBe(want);
  });
});

describe("add and remove", () => {
  test("adds a normalized name once", () => {
    const first = add([], "https://github.com/acme/shop", now);
    expect(first.error).toBeUndefined();
    expect(first.repos).toEqual([{ name: "acme/shop", addedAt: now.toISOString() }]);
    const again = add(first.repos, "acme/shop", now);
    expect(again.error).toContain("already");
    expect(again.repos).toBe(first.repos);
  });

  test("refuses an owner prefix", () => {
    expect(add([], "acme/", now).error).toContain("not an owner/name");
  });

  test("refuses past the cap", () => {
    const full: TrackedRepo[] = Array.from({ length: MAX_TRACKED }, (_, i) => ({
      name: `acme/r${i}`,
      addedAt: now.toISOString(),
    }));
    expect(add(full, "acme/one-more", now).error).toContain("full");
  });

  test("removes by name", () => {
    const { repos } = add([], "acme/shop", now);
    expect(remove(repos, "acme/shop")).toEqual([]);
  });
});

describe("storage", () => {
  test("round-trips through the store", () => {
    const store = new MemoryStore();
    const { repos } = add([], "acme/shop", now);
    expect(save(repos, store)).toBe(true);
    expect(JSON.parse(store.getItem(STORAGE_KEY)!).version).toBe(1);
    expect(load(store)).toEqual(repos);
  });

  test("a throwing store loads empty and reports the save failed", () => {
    const store = new ThrowingStore();
    expect(load(store)).toEqual([]);
    expect(save(add([], "acme/shop", now).repos, store)).toBe(false);
  });

  test("no store at all", () => {
    expect(load(null)).toEqual([]);
    expect(save([], null)).toBe(false);
  });

  test("ignores corrupt and foreign data", () => {
    const store = new MemoryStore();
    store.setItem(STORAGE_KEY, "{not json");
    expect(load(store)).toEqual([]);
    store.setItem(STORAGE_KEY, JSON.stringify({ version: 2, repos: [{ name: "acme/shop" }] }));
    expect(load(store)).toEqual([]);
    store.setItem(
      STORAGE_KEY,
      JSON.stringify({
        version: 1,
        repos: [{ name: "acme/shop", addedAt: "x" }, { name: "acme/" }, { name: "acme/shop" }, 7],
      }),
    );
    expect(load(store).map((r) => r.name)).toEqual(["acme/shop"]);
  });

  test("keeps the discovery source", () => {
    const store = new MemoryStore();
    store.setItem(
      STORAGE_KEY,
      JSON.stringify({ version: 1, repos: [{ name: "acme/shop", addedAt: "t", source: "github" }] }),
    );
    expect(load(store)[0].source).toBe("github");
  });
});

describe("export and import", () => {
  test("an export imports back", () => {
    const { repos } = add([], "acme/shop", now);
    const out = importJSON(exportJSON(repos), [], now);
    expect(out.repos.map((r) => r.name)).toEqual(["acme/shop"]);
    expect(out.added).toBe(1);
  });

  test("merges a bare array, skipping duplicates and invalid names", () => {
    const { repos } = add([], "acme/shop", now);
    const out = importJSON(JSON.stringify(["acme/shop", "acme/api", "acme/", 3]), repos, now);
    expect(out.repos.map((r) => r.name)).toEqual(["acme/shop", "acme/api"]);
    expect(out).toMatchObject({ added: 1, skipped: 3 });
  });

  test("throws on text that is not JSON", () => {
    expect(() => importJSON("nope", [], now)).toThrow();
  });
});

describe("GitHub import", () => {
  const manual = (name: string): TrackedRepo => ({ name, addedAt: now.toISOString() });
  const github = (name: string): TrackedRepo => ({ name, addedAt: now.toISOString(), source: "github" });

  test("adds picked repositories tagged github", () => {
    const res = importGitHub([manual("acme/shop")], ["acme/api", "acme/web"], now);
    expect(res.added).toBe(2);
    expect(res.skipped).toBe(0);
    expect(res.repos).toEqual([manual("acme/shop"), github("acme/api"), github("acme/web")]);
  });

  test("leaves an entry already on the list untouched, in any case", () => {
    const before = [manual("acme/Shop")];
    const res = importGitHub(before, ["acme/shop"], now);
    expect(res).toEqual({ repos: before, added: 0, skipped: 1 });
    expect(res.repos[0].source).toBeUndefined();
    expect(tracks(before, "ACME/SHOP")).toBe(true);
  });

  test("stops at the cap and counts the rest as skipped", () => {
    const start = Array.from({ length: MAX_TRACKED - 2 }, (_, i) => manual(`acme/r${i}`));
    expect(room(start)).toBe(2);
    const res = importGitHub(start, ["acme/a", "acme/b", "acme/c", "acme/d"], now);
    expect(res.added).toBe(2);
    expect(res.skipped).toBe(2);
    expect(res.repos).toHaveLength(MAX_TRACKED);
    expect(room(res.repos)).toBe(0);
    expect(res.repos.slice(-2).map((r) => r.name)).toEqual(["acme/a", "acme/b"]);
  });

  test("skips invalid names", () => {
    const res = importGitHub([], ["acme/ok", "not a repo", "acme/"], now);
    expect(res.added).toBe(1);
    expect(res.skipped).toBe(2);
  });

  test("an imported entry survives storage with its source", () => {
    const store = new MemoryStore();
    const { repos } = importGitHub([], ["acme/api"], now);
    expect(save(repos, store)).toBe(true);
    expect(load(store)).toEqual([github("acme/api")]);
  });
});

describe("GitHub refresh", () => {
  const manual = (name: string): TrackedRepo => ({ name, addedAt: now.toISOString() });
  const github = (name: string): TrackedRepo => ({ name, addedAt: now.toISOString(), source: "github" });

  test("removes only github entries no longer maintained", () => {
    const repos = [manual("acme/kept-manual"), github("acme/still"), github("acme/gone"), manual("acme/also-gone")];
    const res = refreshGitHub(repos, ["acme/still"]);
    expect(res.removed).toEqual(["acme/gone"]);
    expect(res.repos.map((r) => r.name)).toEqual(["acme/kept-manual", "acme/still", "acme/also-gone"]);
  });

  test("matches names case-insensitively", () => {
    const res = refreshGitHub([github("Acme/Shop")], ["acme/shop"]);
    expect(res.removed).toEqual([]);
  });

  test("nothing to remove keeps the same list", () => {
    const repos = [github("acme/a"), manual("acme/b")];
    const res = refreshGitHub(repos, new Set(["acme/a"]));
    expect(res.repos).toBe(repos);
    expect(res.removed).toEqual([]);
  });

  test("an empty maintained list clears every github entry and no manual one", () => {
    const res = refreshGitHub([github("acme/a"), manual("acme/b"), github("acme/c")], []);
    expect(res.removed).toEqual(["acme/a", "acme/c"]);
    expect(res.repos).toEqual([manual("acme/b")]);
  });
});
