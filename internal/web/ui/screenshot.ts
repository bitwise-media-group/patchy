// Screenshots of the status page for the docs site, in both colour schemes.
//
//   bun screenshot.ts [--out <dir>] [--chrome <path>]
//   mise run ui-screenshots
//
// Starts the dev server in --demo mode (the built-in mock data, no status
// server needed), launches headless Chrome with a remote-debugging port, and
// drives it over the DevTools protocol: for each shot and each scheme it
// emulates prefers-color-scheme (the page's pre-paint script picks the theme
// from it when nothing is stored) and reduced motion (so the live dot is
// still), loads the page at 1440×900, waits for fonts and a quiet moment, and
// saves a JPEG (<name>.jpg dark, <name>-light.jpg light). Two sets, under
// --out:
//
//   status-{findings,finding-detail,metrics}  the docs' standing screenshots
//   whats-new/<version>/<name>                the current What's new page's,
//                                             as its front matter declares
//
// docs/whats-new/index.md declares its own shots, and the version they file
// under, in its front matter:
//
//   version: 0.15.0 # x-release-please-version
//   screenshots:
//     - name: filter-typeahead   # the image's file name
//       hash: "#/"               # the route to load
//       type: mobile             # optional: typed into the findings filter
//
// The freeze after a release carries the list into the frozen page and
// starts the next page with none, so a placeholder page shoots nothing and an
// earlier release's images are never shot again.
//
// --out defaults to the git-ignored preview folder internal/web/ui/screenshots/.
// Only the release-PR workflow (.github/workflows/release-screenshots.yaml)
// writes into docs/assets/images/ and commits the result, so a dev run can
// never change a committed image.
//
// Chrome is found from --chrome, $CHROME_PATH, the macOS app bundle, or
// google-chrome / chromium / chrome on PATH. It cannot run inside the Claude
// Code sandbox, so run this from a normal shell.

import { mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { parseArgs } from "node:util";

const { values } = parseArgs({
  args: Bun.argv.slice(2),
  options: {
    out: { type: "string", default: resolve(import.meta.dir, "screenshots") },
    chrome: { type: "string" },
  },
});

const WIDTH = 1440;
const HEIGHT = 900;

interface Shot {
  name: string;
  hash: string;
  // Typed into the findings filter after load, to open its suggestions.
  type?: string;
  // The directory the JPEGs are written to.
  dir: string;
}

const STANDING: Shot[] = [
  { name: "status-findings", hash: "#/", dir: values.out! },
  // The mock finding held in AwaitingApproval, so the action bar shows.
  { name: "status-finding-detail", hash: "#/finding/gh-cs-dotty-88af02", dir: values.out! },
  { name: "status-metrics", hash: "#/metrics", dir: values.out! },
];

// whatsNewShots reads the current What's new page's front matter: its
// version (the release being written up, which the freeze workflow sets and
// the release PR corrects) and the screenshots it declares.
function whatsNewShots(): Shot[] {
  const page = resolve(import.meta.dir, "../../../docs/whats-new/index.md");
  const raw = /^---\n([\s\S]*?)\n---\n/.exec(readFileSync(page, "utf8"))?.[1];
  if (raw === undefined) throw new Error(`${page} has no front matter`);
  const matter = Bun.YAML.parse(raw) as { version?: unknown; screenshots?: unknown };
  const version = String(matter.version ?? "");
  if (!/^\d+\.\d+\.\d+$/.test(version)) throw new Error(`${page}: front matter version "${version}" is not x.y.z`);
  const list = matter.screenshots ?? [];
  if (!Array.isArray(list)) throw new Error(`${page}: front matter screenshots must be a list`);
  const dir = join(values.out!, "whats-new", version);
  const seen = new Set<string>();
  return list.map((entry: unknown, i: number) => {
    const s = (entry ?? {}) as Record<string, unknown>;
    const where = `${page}: screenshots[${i}]`;
    if (typeof s.name !== "string" || !/^[a-z0-9][a-z0-9-]*$/.test(s.name) || s.name.endsWith("-light")) {
      throw new Error(`${where}: name must be lower-case kebab-case and not end in -light (the light variant's suffix)`);
    }
    if (seen.has(s.name)) throw new Error(`${where}: name ${s.name} is used twice`);
    seen.add(s.name);
    if (typeof s.hash !== "string" || !s.hash.startsWith("#/")) throw new Error(`${where}: hash must be a route like "#/"`);
    if (s.type !== undefined && typeof s.type !== "string") throw new Error(`${where}: type must be text`);
    return { name: s.name, hash: s.hash, type: s.type, dir };
  });
}

const ROUTES = [...STANDING, ...whatsNewShots()];
for (const dir of new Set(ROUTES.map((r) => r.dir))) mkdirSync(dir, { recursive: true });
// FIXED_CLOCK pins Date (now, and new Date() with no arguments) to DEMO_NOW
// before any page script runs. Timers are untouched.
const DEMO_NOW = "2026-07-21T09:00:00Z";
const FIXED_CLOCK = `(() => {
  const Real = Date;
  const fixed = Real.parse(${JSON.stringify(DEMO_NOW)});
  function FixedDate(...args) {
    if (!new.target) return new Real(fixed).toString();
    return args.length ? new Real(...args) : new Real(fixed);
  }
  Object.setPrototypeOf(FixedDate, Real);
  FixedDate.prototype = Real.prototype;
  FixedDate.now = () => fixed;
  window.Date = FixedDate;
})();`;

const SCHEMES = [
  { scheme: "dark", suffix: "" },
  { scheme: "light", suffix: "-light" },
];

function findChrome(): string {
  const candidates = [
    values.chrome,
    process.env.CHROME_PATH,
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
    Bun.which("google-chrome"),
    Bun.which("chromium"),
    Bun.which("chrome"),
  ];
  for (const c of candidates) {
    if (c && Bun.file(c).size > 0) return c;
  }
  throw new Error("no Chrome found: pass --chrome <path> or set CHROME_PATH");
}

function freePort(): number {
  const probe = Bun.serve({ port: 0, fetch: () => new Response() });
  const port = probe.port;
  probe.stop(true);
  if (port === undefined) throw new Error("could not find a free port");
  return port;
}

async function waitFor<T>(what: string, probe: () => Promise<T | undefined>, timeoutMs = 30_000): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const v = await probe();
      if (v !== undefined) return v;
    } catch {
      // not up yet
    }
    await Bun.sleep(200);
  }
  throw new Error(`timed out waiting for ${what}`);
}

// A minimal DevTools-protocol client: numbered requests, and one-shot waits
// for named events.
class CDP {
  private ws: WebSocket;
  private next = 1;
  private pending = new Map<number, { resolve: (v: any) => void; reject: (e: Error) => void }>();
  private waiters = new Map<string, Array<() => void>>();

  private constructor(ws: WebSocket) {
    this.ws = ws;
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(String(ev.data));
      if (msg.id !== undefined) {
        const p = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (msg.error) p?.reject(new Error(`${msg.error.message} (${msg.error.code})`));
        else p?.resolve(msg.result);
      } else if (msg.method) {
        const ws = this.waiters.get(msg.method) ?? [];
        this.waiters.delete(msg.method);
        for (const w of ws) w();
      }
    });
  }

  static open(url: string): Promise<CDP> {
    return new Promise((res, rej) => {
      const ws = new WebSocket(url);
      ws.addEventListener("open", () => res(new CDP(ws)));
      ws.addEventListener("error", () => rej(new Error(`cannot connect to ${url}`)));
    });
  }

  send(method: string, params: Record<string, unknown> = {}): Promise<any> {
    const id = this.next++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }

  once(method: string, timeoutMs = 30_000): Promise<void> {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`timed out waiting for ${method}`)), timeoutMs);
      const list = this.waiters.get(method) ?? [];
      list.push(() => {
        clearTimeout(timer);
        resolve();
      });
      this.waiters.set(method, list);
    });
  }

  close() {
    this.ws.close();
  }
}

const webPort = freePort();
const debugPort = freePort();
const profile = mkdtempSync(join(tmpdir(), "patchy-shots-"));

const dev = Bun.spawn(["bun", "dev.ts", "--demo", "--port", String(webPort)], {
  cwd: import.meta.dir,
  stdout: "ignore",
  stderr: "inherit",
});
const chrome = Bun.spawn(
  [
    findChrome(),
    "--headless=new",
    `--remote-debugging-port=${debugPort}`,
    `--user-data-dir=${profile}`,
    `--window-size=${WIDTH},${HEIGHT}`,
    "--no-first-run",
    "--no-default-browser-check",
    "--hide-scrollbars",
    "--force-color-profile=srgb",
    // Ubuntu 24.04 (the GitHub runners) restricts the unprivileged user
    // namespaces Chrome's sandbox needs; the page is our own demo.
    ...(process.env.CI ? ["--no-sandbox"] : []),
    "about:blank",
  ],
  { stdout: "ignore", stderr: "ignore" },
);

let cdp: CDP | undefined;
try {
  const base = `http://127.0.0.1:${webPort}/`;
  await waitFor("the dev server", async () => ((await fetch(base)).ok ? true : undefined));
  const targets = await waitFor("Chrome", async () => {
    const list = (await (await fetch(`http://127.0.0.1:${debugPort}/json/list`)).json()) as Array<{
      type: string;
      webSocketDebuggerUrl: string;
    }>;
    return list.find((t) => t.type === "page");
  });
  cdp = await CDP.open(targets.webSocketDebuggerUrl);
  await cdp.send("Page.enable");
  // Freeze the page's clock just after the demo data's generatedAt, so
  // relative times ("12 min ago") and so the images are the same on every
  // run: the release-PR workflow commits only images that changed.
  await cdp.send("Page.addScriptToEvaluateOnNewDocument", { source: FIXED_CLOCK });
  await cdp.send("Emulation.setDeviceMetricsOverride", {
    width: WIDTH,
    height: HEIGHT,
    deviceScaleFactor: 1,
    mobile: false,
  });

  for (const { scheme, suffix } of SCHEMES) {
    await cdp.send("Emulation.setEmulatedMedia", {
      features: [
        { name: "prefers-color-scheme", value: scheme },
        { name: "prefers-reduced-motion", value: "reduce" },
      ],
    });
    for (const route of ROUTES) {
      // Through about:blank, so a hash-only change still reloads the page
      // and the pre-paint script re-reads the scheme.
      await cdp.send("Page.navigate", { url: "about:blank" });
      const loaded = cdp.once("Page.loadEventFired");
      await cdp.send("Page.navigate", { url: base + route.hash });
      await loaded;
      await cdp.send("Runtime.evaluate", { expression: "document.fonts.ready.then(() => true)", awaitPromise: true });
      if (route.type) {
        await cdp.send("Runtime.evaluate", {
          expression: `document.querySelector('input[aria-label="Filter findings"]').focus()`,
        });
        await cdp.send("Input.insertText", { text: route.type });
      }
      await Bun.sleep(1200);
      const shot = await cdp.send("Page.captureScreenshot", { format: "jpeg", quality: 85 });
      const file = join(route.dir, `${route.name}${suffix}.jpg`);
      await Bun.write(file, Buffer.from(shot.data as string, "base64"));
      console.log(`wrote ${file}`);
    }
  }
} finally {
  cdp?.close();
  chrome.kill();
  dev.kill();
  await Promise.allSettled([chrome.exited, dev.exited]);
  rmSync(profile, { recursive: true, force: true });
}
