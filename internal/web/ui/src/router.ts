// Hash routing, so the page works from a static file server, over file://,
// and inside the embedded server without rewrites.
//
//   #/                    findings list
//   #/finding/{name}      finding detail (optional /{tab})
//   #/metrics             metrics, optionally sliced: /{kind}/{key}, where
//                         kind is repository | model | harness | match (an
//                         aggregate of every repository matching key).
//                         #/rollups/… is the old name and still resolves.
//   #/config              configuration, optional /{section}

import { useEffect, useState } from "preact/hooks";
import type { RollupSlice, SliceKind } from "./rollups";

export type TabId = "overview" | "alerts" | "timeline" | "investigation" | "remediation";

export type ConfigSection = "forges" | "integrations" | "enhancers";

export type Route =
  | { view: "list" }
  | { view: "detail"; name: string; tab: TabId }
  | { view: "metrics"; slice: RollupSlice | null }
  | { view: "config"; section: ConfigSection };

const TABS: TabId[] = ["overview", "alerts", "timeline", "investigation", "remediation"];
const SLICE_KINDS: SliceKind[] = ["repository", "model", "harness", "match"];
const SECTIONS: ConfigSection[] = ["forges", "integrations", "enhancers"];

export function parseRoute(hash: string): Route {
  const parts = hash.replace(/^#\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
  if (parts[0] === "finding" && parts[1]) {
    const tab = TABS.includes(parts[2] as TabId) ? (parts[2] as TabId) : "overview";
    return { view: "detail", name: parts[1], tab };
  }
  if (parts[0] === "metrics" || parts[0] === "rollups") {
    // Repository keys contain "/", so a key arrives as one encoded segment;
    // the pre-filter scope links (#/rollups/repository) land on the
    // unsliced view.
    const kind = parts[1] as SliceKind;
    const key = parts.slice(2).join("/");
    const slice = SLICE_KINDS.includes(kind) && key !== "" ? { kind, key } : null;
    return { view: "metrics", slice };
  }
  if (parts[0] === "config") {
    const section = SECTIONS.includes(parts[1] as ConfigSection)
      ? (parts[1] as ConfigSection)
      : "integrations";
    return { view: "config", section };
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

export function hrefForMetrics(slice?: RollupSlice | null): string {
  return slice ? `#/metrics/${slice.kind}/${encodeURIComponent(slice.key)}` : "#/metrics";
}

export function hrefForConfig(section?: ConfigSection): string {
  return section && section !== "integrations" ? `#/config/${section}` : "#/config";
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
