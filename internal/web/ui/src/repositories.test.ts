import { describe, expect, test } from "bun:test";
import { PHASE_GROUPS, accessProblems, accessReason, findingsHrefFor, forgeBadge, ownerGroups, findingsHref, forgeProblem, groupCount, metricsHref, neverSeen } from "./repositories";
import { selectionFromSearch } from "./filters";
import { parseRoute } from "./router";
import { PHASE_ORDER, TERMINAL_PHASES } from "./types";

describe("phase groups", () => {
  test("cover every phase exactly once", () => {
    const grouped = PHASE_GROUPS.flatMap((g) => g.phases).sort();
    const all = [...new Set([...PHASE_ORDER, ...TERMINAL_PHASES])].sort();
    expect(grouped).toEqual(all);
  });

  test("count the phases of their group", () => {
    const summary = { name: "acme/shop", findings: 4, phases: { Queued: 2, Remediating: 1, Failed: 1 } };
    const progress = PHASE_GROUPS.find((g) => g.id === "progress")!;
    expect(groupCount(summary, progress)).toBe(3);
    expect(groupCount(undefined, progress)).toBe(0);
  });
});

describe("links", () => {
  test("a count opens the list filtered to the repository and its phases", () => {
    const href = findingsHref("acme/shop", ["Remediating", "Queued"]);
    expect(href).toBe("#/?phase=Queued&phase=Remediating&repo=acme%2Fshop");
    const route = parseRoute(href);
    expect(route.view).toBe("list");
    const sel = selectionFromSearch(route.view === "list" ? route.query : "");
    expect([...sel.repos]).toEqual(["acme/shop"]);
    expect([...sel.phases].sort()).toEqual(["Queued", "Remediating"]);
  });

  test("the name opens the list filtered to the repository", () => {
    expect(findingsHref("acme/shop")).toBe("#/?repo=acme%2Fshop");
  });

  test("the metrics link opens the repository slice", () => {
    const route = parseRoute(metricsHref("acme/shop"));
    expect(route).toEqual({ view: "metrics", slice: { kind: "repository", key: "acme/shop" } });
  });

  test("the page has its own route", () => {
    expect(parseRoute("#/repositories")).toEqual({ view: "repositories" });
  });
});

describe("verdicts", () => {
  test("never seen means no findings and no rollup", () => {
    expect(neverSeen({ name: "a/b", findings: 0 })).toBe(true);
    expect(neverSeen({ name: "a/b", findings: 1 })).toBe(false);
    expect(neverSeen({ name: "a/b", findings: 0, rollup: { findings: 3 } })).toBe(false);
    expect(neverSeen(undefined)).toBe(false);
  });

  test("explains an uninstalled App once", () => {
    const lines = accessProblems({
      name: "acme/shop",
      integrations: [
        {
          name: "gh",
          alerts: { codeScanning: { status: "not_installed" }, secretScanning: { status: "not_installed" } },
        },
      ],
    });
    expect(lines).toEqual(["The GitHub App of integration gh is not installed on acme/shop."]);
  });

  test("names the missing permission", () => {
    const [line] = accessProblems({
      name: "acme/shop",
      integrations: [{ name: "gh", alerts: { codeScanning: { status: "forbidden", detail: "nope" } } }],
    });
    expect(line).toContain("security_events: read");
    expect(line).toContain("nope");
  });

  test("no integration at all", () => {
    expect(accessProblems({ name: "acme/shop", integrations: [] })[0]).toContain("No GitHub integration");
  });

  test("forge gaps", () => {
    expect(forgeProblem("acme/shop", { status: "ok", contents: "write" })).toBeNull();
    expect(forgeProblem("acme/shop", undefined)).toBeNull();
    expect(forgeProblem("acme/shop", { status: "missing" })).toContain("No Forge");
    expect(forgeProblem("acme/shop", { name: "github", status: "no_write", contents: "read" })).toContain(
      "contents: write",
    );
  });
});

describe("short labels", () => {
  const one = (codeScanning: object, secretScanning?: object) => ({
    name: "acme/shop",
    integrations: [{ name: "gh", alerts: { codeScanning, secretScanning } }],
  });
  test.each([
    [one({ status: "not_installed" }), "App not installed"],
    [one({ status: "forbidden", detail: "Advanced Security must be enabled" }), "Advanced Security off"],
    [one({ status: "forbidden", detail: "Resource not accessible by integration" }), "missing permission"],
    [one({ status: "no_analysis" }), "never scanned"],
    [one({ status: "not_found", detail: "Secret scanning is disabled on this repository." }), "scanning disabled"],
    [one({ status: "error" }), "check failed"],
    [{ name: "acme/shop", integrations: [] }, "no scanning integration"],
  ])("%#: %p", (access, want) => {
    expect(accessReason(access as never)).toBe(want);
  });

  test("forge badges", () => {
    expect(forgeBadge(undefined)).toBeNull();
    expect(forgeBadge({ status: "ok" })).toBeNull();
    expect(forgeBadge({ status: "missing" })).toBe("No Forge");
    expect(forgeBadge({ status: "no_write", contents: "read" })).toBe("Forge is read-only");
  });
});

describe("grouping and multi-repository links", () => {
  test("groups by owner in first-seen order", () => {
    expect(ownerGroups(["b/x", "a/y", "b/z"])).toEqual([
      { owner: "b", repos: ["b/x", "b/z"] },
      { owner: "a", repos: ["a/y"] },
    ]);
  });

  test("filters the list to every tracked repository", () => {
    expect(findingsHrefFor(["b/x", "a/y"], ["Failed"])).toBe("#/?phase=Failed&repo=a%2Fy&repo=b%2Fx");
  });
});
