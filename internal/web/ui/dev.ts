// Development server for the status page: Bun's bundler serves index.html with
// hot reload, and the server-backed paths are forwarded to a running status
// server, so the page works against real data while its source is edited.
//
//   PATCHY_API_TARGET=http://127.0.0.1:8080 bun run dev -- --port 5173
//
// --demo needs no server at all: the page runs on its built-in mock data
// (src/mock: findings, rollups, config, transcripts, and the persona switcher
// for previewing permissions), and nothing is forwarded.
//
// Tailwind is applied through bunfig.toml ([serve.static] plugins), which is
// how the HTML dev server loads bundler plugins.

import { parseArgs } from "node:util";
import index from "./index.html";

const { values } = parseArgs({
  args: Bun.argv.slice(2),
  options: {
    port: { type: "string", default: "5173" },
    demo: { type: "boolean", default: false },
  },
});
const demo = values.demo;
const port = Number(values.port);
const target = new URL(process.env.PATCHY_API_TARGET ?? "http://127.0.0.1:8080");

// Hop-by-hop headers, plus the two that stop being true once fetch has
// decoded the upstream body: passing content-encoding through would make the
// browser decompress a body that is no longer compressed.
const DROP = ["connection", "keep-alive", "transfer-encoding", "content-encoding", "content-length"];

async function forward(req: Request): Promise<Response> {
  const url = new URL(req.url);
  const upstream = new URL(url.pathname + url.search, target);
  const headers = new Headers(req.headers);
  headers.set("host", upstream.host);
  try {
    const res = await fetch(upstream, {
      method: req.method,
      headers,
      body: req.method === "GET" || req.method === "HEAD" ? undefined : req.body,
      // Sign-in redirects belong to the browser, not to this proxy.
      redirect: "manual",
      signal: req.signal,
    });
    const out = new Headers(res.headers);
    for (const name of DROP) out.delete(name);
    return new Response(res.body, { status: res.status, statusText: res.statusText, headers: out });
  } catch (err) {
    warnUnreachable(err);
    return new Response(`status server at ${target.origin} unreachable: ${err}\n`, { status: 502 });
  }
}

// The page only shows "HTTP 502", so say why in the terminal (once per
// outage, not once per request) with the way to bring a status server up.
let warned = false;
function warnUnreachable(err: unknown) {
  if (warned) return;
  warned = true;
  console.warn(
    `\nno status server at ${target.origin} (${err}).\n` +
      "  The page needs one for its data. Forward the cluster's:\n" +
      `    kubectl -n patchy port-forward svc/patchy-status-server ${target.port || 8080}:8080\n` +
      "  or point PATCHY_API_TARGET at another, e.g.\n" +
      "    PATCHY_API_TARGET=http://127.0.0.1:9090 mise run ui-dev\n" +
      "  or run on mock data, with no server at all:\n" +
      "    mise run ui-dev -- --demo\n",
  );
  setTimeout(() => (warned = false), 30_000);
}

// Demo mode is the page's own: api.ts serves mock data whenever
// window.__PATCHY_STATUS_DEMO__ is set, and makes no requests. The flag is
// injected here, into the served HTML only, so index.html and the production
// bundle never carry it. The bundle is mounted at a private path and "/"
// rewrites it on the way out; its asset and hot-reload URLs are absolute, so
// they resolve the same from either.
const PAGE = "/__page";
const DEMO_FLAG = "<script>window.__PATCHY_STATUS_DEMO__ = true;</script>";

async function demoPage(): Promise<Response> {
  const page = await fetch(new URL(PAGE, server.url));
  return new HTMLRewriter()
    .on("head", { element: (head) => void head.prepend(DEMO_FLAG, { html: true }) })
    .transform(page);
}

type Route = Bun.HTMLBundle | ((req: Request) => Response | Promise<Response>);
const routes: Record<string, Route> = demo
  ? { [PAGE]: index, "/": demoPage }
  : { "/": index, "/api/*": forward, "/events": forward, "/oauth2/*": forward, "/logout": forward };

const server = Bun.serve({
  port,
  // The status server pings its event streams every 25s; Bun's default idle
  // timeout (10s) would cut every live transcript and dataset stream.
  idleTimeout: 0,
  development: { hmr: true, console: true },
  routes,
  fetch: () => new Response("Not found\n", { status: 404 }),
});

if (demo) {
  console.log(`status page dev server on ${server.url} (demo: mock data, no status server)`);
} else {
  console.log(`status page dev server on ${server.url} (API: ${target.origin})`);
  // Probe the target up front, so a missing status server is reported before
  // the first page load rather than discovered from it.
  fetch(new URL("/api/rollups", target), { signal: AbortSignal.timeout(2000) }).catch(warnUnreachable);
}
