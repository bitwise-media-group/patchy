import { describe, expect, test } from "bun:test";
import {
  MAX_TRACKED,
  STORAGE_KEY,
  add,
  exportJSON,
  importJSON,
  load,
  normalizeRepoName,
  remove,
  save,
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
