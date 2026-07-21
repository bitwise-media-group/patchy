// Hash routing — works from a static file server, over file://, and inside
// the embedded server without rewrites.
//
//   #/                    findings list
//   #/finding/{name}      finding detail (optional /{tab})
//   #/rollups             rollups, optional /{scope}

import { useEffect, useState } from "preact/hooks";
import type { ScopeType } from "./types";

export type TabId = "overview" | "alerts" | "timeline" | "remediation";

export type Route =
  | { view: "list" }
  | { view: "detail"; name: string; tab: TabId }
  | { view: "rollups"; scope: ScopeType };

const TABS: TabId[] = ["overview", "alerts", "timeline", "remediation"];
const SCOPES: ScopeType[] = ["total", "repository", "harness", "model"];

export function parseRoute(hash: string): Route {
  const parts = hash.replace(/^#\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
  if (parts[0] === "finding" && parts[1]) {
    const tab = TABS.includes(parts[2] as TabId) ? (parts[2] as TabId) : "overview";
    return { view: "detail", name: parts[1], tab };
  }
  if (parts[0] === "rollups") {
    const scope = SCOPES.includes(parts[1] as ScopeType) ? (parts[1] as ScopeType) : "total";
    return { view: "rollups", scope };
  }
  return { view: "list" };
}

export function hrefForList(): string {
  return "#/";
}

export function hrefForFinding(name: string, tab?: TabId): string {
  const base = `#/finding/${encodeURIComponent(name)}`;
  return tab && tab !== "overview" ? `${base}/${tab}` : base;
}

export function hrefForRollups(scope?: ScopeType): string {
  return scope && scope !== "total" ? `#/rollups/${scope}` : "#/rollups";
}

export function navigate(href: string): void {
  window.location.hash = href.replace(/^#/, "");
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(() => parseRoute(window.location.hash));
  useEffect(() => {
    const onChange = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener("hashchange", onChange);
    return () => window.removeEventListener("hashchange", onChange);
  }, []);
  return route;
}
