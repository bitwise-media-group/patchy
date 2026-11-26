import { describe, expect, test } from "bun:test";
import {
  PHASE_GROUPS,
  accessProblems,
  accessReason,
  anyConnected,
  backfillAvailable,
  backfillMode,
  backfillOpen,
  backfillOutcomeMessage,
  backfillPhase,
  backfillState,
  filterImportable,
  findingsHrefFor,
  forgeBadge,
  githubConnectHref,
  githubHint,
  githubProfileHref,
  ownerGroups,
  findingsHref,
  forgeProblem,
  groupCount,
  metricsHref,
  neverSeen,
} from "./repositories";
import { formatIn } from "./format";
import type { GitHubLink, RepositoriesPayload } from "./types";
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

describe("backfill mode", () => {
  const connected: GitHubLink[] = [{ integration: "gh", host: "github.com", login: "mona", connected: true }];
  const unlinked: GitHubLink[] = [{ integration: "gh", host: "github.com", connected: false }];
  type Grants = Pick<RepositoriesPayload, "canBackfill" | "canBackfillMaintained" | "canRequestBackfill" | "github">;
  const g = (p: Partial<Grants>): Grants => ({ canBackfill: false, canRequestBackfill: false, ...p });

  test.each<[string, Grants, boolean, string]>([
    ["the backfill grant", g({ canBackfill: true, canRequestBackfill: true }), false, "backfill"],
    ["the grant needs no GitHub", g({ canBackfill: true, github: unlinked }), false, "backfill"],
    ["the grant ignores a refusal", g({ canBackfill: true }), true, "backfill"],
    ["maintained, connected", g({ canBackfillMaintained: true, canRequestBackfill: true, github: connected }), false, "backfill"],
    ["maintained, not connected", g({ canBackfillMaintained: true, canRequestBackfill: true, github: unlinked }), false, "request"],
    ["maintained, linking off", g({ canBackfillMaintained: true, canRequestBackfill: true }), false, "request"],
    ["maintained, refused", g({ canBackfillMaintained: true, canRequestBackfill: true, github: connected }), true, "request"],
    ["maintained, refused, no request", g({ canBackfillMaintained: true, github: connected }), true, "none"],
    ["request only", g({ canRequestBackfill: true }), false, "request"],
    ["nothing", g({}), false, "none"],
  ])("%s", (_, grants, refused, want) => {
    expect(backfillMode(grants, refused)).toBe(want as never);
  });

  test("no payload yet offers nothing", () => {
    expect(backfillMode(null)).toBe("none");
  });

  test("any connected link counts", () => {
    expect(anyConnected(undefined)).toBe(false);
    expect(anyConnected(unlinked)).toBe(false);
    expect(anyConnected([...unlinked, ...connected])).toBe(true);
  });
});

describe("backfill cooldown", () => {
  const now = Date.parse("2026-11-25T10:00:00Z");
  test("absent means now", () => {
    expect(backfillAvailable({ name: "a/b", findings: 0 }, now)).toBe(true);
    expect(backfillAvailable(undefined, now)).toBe(true);
  });
  test("a time to come holds it", () => {
    expect(backfillAvailable({ name: "a/b", findings: 0, backfillAvailableAt: "2026-11-26T07:00:00Z" }, now)).toBe(false);
    expect(formatIn("2026-11-26T07:00:00Z", now)).toBe("in 21h");
    expect(formatIn("2026-11-25T10:20:00Z", now)).toBe("in 20m");
    expect(formatIn("2026-11-28T10:00:00Z", now)).toBe("in 3d");
  });
  test("a time past releases it", () => {
    expect(backfillAvailable({ name: "a/b", findings: 0, backfillAvailableAt: "2026-11-25T09:59:00Z" }, now)).toBe(true);
    expect(formatIn("2026-11-25T09:59:00Z", now)).toBe("now");
  });
  test("an unreadable time does not lock the button", () => {
    expect(backfillAvailable({ name: "a/b", findings: 0, backfillAvailableAt: "soon" }, now)).toBe(true);
  });
});

describe("backfill state", () => {
  const ago = () => "2h ago";
  const base = { integration: "gh", requestedBy: "sam", requestedAt: "2026-11-25T08:00:00Z" };

  test("awaiting approval says an operator decides", () => {
    const st = backfillState({ ...base, phase: "AwaitingApproval", pending: true }, ago);
    expect(st.label).toBe("Awaiting approval");
    expect(st.tone).toBe("amber");
    expect(st.text).toBe("Requested 2h ago by sam. An operator will review it before it runs.");
  });

  test("pending waits for the controller", () => {
    const st = backfillState({ ...base, phase: "Pending", pending: true }, ago);
    expect(st.label).toBe("Backfill pending");
    expect(st.text).toContain("Requested on gh 2h ago by sam");
  });

  test("complete reports its counts", () => {
    const st = backfillState({ ...base, phase: "Complete", lastRunAt: "x", listed: 12, ingested: 3 }, ago);
    expect(st.tone).toBe("green");
    expect(st.text).toBe("Backfilled on gh 2h ago: listed 12 open alerts, ingested 3.");
    expect(backfillState({ ...base, phase: "Complete", lastRunAt: "x" }, ago).text).toBe(
      "Backfilled on gh 2h ago: it had no open alerts.",
    );
  });

  test("failed shows the error", () => {
    const st = backfillState({ ...base, phase: "Failed", error: "rate limited" }, ago);
    expect(st.label).toBe("Backfill failed");
    expect(st.text).toBe("rate limited");
  });

  test("denied quotes the operator's reason", () => {
    expect(backfillState({ ...base, phase: "Denied", decisionReason: "retired" }, ago).text).toBe(
      "An operator said: “retired”",
    );
    expect(backfillState({ ...base, phase: "Denied" }, ago).text).toBe("An operator denied the request.");
  });

  test("a phaseless answer reads from pending and error", () => {
    expect(backfillPhase({ ...base, pending: true })).toBe("Pending");
    expect(backfillPhase({ ...base, error: "x" })).toBe("Failed");
    expect(backfillPhase(base)).toBe("Complete");
  });

  test("open means awaiting a decision or a walk", () => {
    expect(backfillOpen(undefined)).toBe(false);
    expect(backfillOpen({ ...base, phase: "AwaitingApproval" })).toBe(true);
    expect(backfillOpen({ ...base, phase: "Pending" })).toBe(true);
    expect(backfillOpen({ ...base, phase: "Complete" })).toBe(false);
    expect(backfillOpen({ ...base, phase: "Denied" })).toBe(false);
    expect(backfillOpen({ ...base, phase: "Failed" })).toBe(false);
  });
});

describe("backfill outcomes", () => {
  test.each([
    ["requested", "Backfill of acme/shop started on gh. Its open alerts arrive as findings shortly."],
    ["awaiting-approval", "Backfill of acme/shop requested. An operator will review it before it runs."],
    ["covered", "A backfill already open on gh covers acme/shop."],
  ] as const)("%s", (outcome, want) => {
    expect(backfillOutcomeMessage("acme/shop", { integration: "gh", request: "backfill-x", outcome })).toBe(want);
  });
});

describe("GitHub link", () => {
  test("connect returns to the page, path and hash", () => {
    expect(githubConnectHref("gh", "/#/repositories")).toBe(
      "/oauth2/github/gh/authorize?originalPath=%2F%23%2Frepositories",
    );
  });

  test("a connected login links its profile on the host", () => {
    expect(githubProfileHref({ integration: "ghe", host: "ghe.acme.internal", login: "mona", connected: true })).toBe(
      "https://ghe.acme.internal/mona",
    );
    expect(githubProfileHref({ integration: "gh", host: "github.com", connected: false })).toBeUndefined();
  });

  test("the picker filters by name, ignoring case", () => {
    const repos = [{ name: "acme/Shop" }, { name: "acme/api" }, { name: "other/shopfront" }];
    expect(filterImportable(repos, " SHOP ").map((r) => r.name)).toEqual(["acme/Shop", "other/shopfront"]);
    expect(filterImportable(repos, "")).toBe(repos);
  });
});

describe("Connect GitHub hint", () => {
  const off = { integration: "gh", host: "github.com", connected: false };
  const on = { ...off, login: "mona", connected: true };
  test.each([
    ["scoped approve, nothing connected", { maintainedActions: ["approve-maintained"], github: [off] }, true],
    ["scoped expedite, nothing connected", { maintainedActions: ["expedite-maintained"], github: [off] }, true],
    ["connected already", { maintainedActions: ["approve-maintained"], github: [off, on] }, false],
    ["only the scoped backfill", { maintainedActions: ["backfill-maintained"], github: [off] }, false],
    ["linking off", { maintainedActions: ["approve-maintained"] }, false],
    ["no scoped grants", { github: [off] }, false],
  ] as const)("%s", (_, user, want) => {
    expect(githubHint({ name: "u", loggedIn: true, ...(user as object) })).toBe(want);
  });
  test("no user", () => {
    expect(githubHint(undefined)).toBe(false);
  });
});
