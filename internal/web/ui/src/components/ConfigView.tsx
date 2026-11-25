// The configuration view: how patchy is wired up in this namespace, meaning
// the Integrations findings arrive through, the context enhancers they enable,
// and the Forges code is fetched and changed through. One page rather than
// sub-tabs (there are only ever a handful of each), opening with a health
// summary and the flow between the three, so "is everything connected?" is
// answered before any detail.
//
// Read-only except the backfill trigger, which renders only when the server
// granted the backfill verb (user.integrationActions), so read-only users
// never see a disabled button. The trigger's two-click confirm follows
// ActionBar's idiom (no modals).

import type { ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { ConfigDataset, EnhancerConfig, ForgeConfig, IntegrationConfig } from "../types";
import { formatAgo, formatCount, formatDate } from "../format";
import { hrefForConfig, type ConfigSection } from "../router";
import { Icon } from "./icons";
import { BackfillButton } from "./BackfillButton";

// ---- vocabulary -------------------------------------------------------------

const PROVIDERS: Record<string, string> = {
  github: "GitHub",
  "google-cloud": "Google Cloud",
  wiz: "Wiz",
  aws: "AWS",
  azure: "Azure",
  generic: "Generic HTTP",
};

const MONOGRAMS: Record<string, string> = {
  "google-cloud": "GC",
  wiz: "Wz",
  aws: "AWS",
  azure: "Az",
  generic: "{ }",
};

// Capability names as internal/web/config.go emits them. "issues" means a
// different thing per provider: GitHub projects tracking issues out, Wiz
// feeds its Issues in.
function capabilityLabel(provider: string, cap: string): string {
  if (cap === "issues") return provider === "github" ? "Tracking issues" : "Issues feed";
  const labels: Record<string, string> = {
    codeScanningAlerts: "Code scanning alerts",
    secretScanningAlerts: "Secret scanning alerts",
    redelivery: "Webhook redelivery",
    securityCommandCenter: "Security Command Center",
    cloudAssetInventory: "Cloud Asset Inventory",
    defend: "Wiz Defend",
    api: "Wiz API",
    resourceTags: "Resource tags",
    source: "Finding source",
    enhance: "Context lookup",
  };
  return labels[cap] ?? cap.replace(/([a-z])([A-Z])/g, "$1 $2").replace(/^./, (c) => c.toUpperCase());
}

// SOURCE_CAPS are the capabilities that bring findings in.
const SOURCE_CAPS = new Set([
  "codeScanningAlerts",
  "secretScanningAlerts",
  "securityCommandCenter",
  "defend",
  "source",
]);
const isSource = (i: IntegrationConfig) =>
  (i.capabilities ?? []).some((c) => SOURCE_CAPS.has(c) || (c === "issues" && i.provider === "wiz"));

const ENHANCERS: Record<string, string> = {
  "google-cloud-labels": "Google Cloud asset labels",
  "aws-resource-tags": "AWS resource tags",
  "azure-resource-tags": "Azure resource tags",
  generic: "Generic HTTP lookup",
};

// humanInterval renders a Go duration ("10m0s", "1h30m0s") as prose.
function humanInterval(d?: string): string | undefined {
  if (!d) return undefined;
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?$/.exec(d);
  if (!m) return d;
  const parts: string[] = [];
  const unit = (n: number, u: string) => `${n} ${u}${n === 1 ? "" : "s"}`;
  if (m[1] && +m[1]) parts.push(unit(+m[1], "hour"));
  if (m[2] && +m[2]) parts.push(unit(+m[2], "minute"));
  if (m[3] && +m[3]) parts.push(unit(+m[3], "second"));
  return parts.length ? `Every ${parts.join(" ")}` : d;
}

// ---- health -------------------------------------------------------------------

type Tone = "green" | "amber" | "red" | "neutral";

interface Health {
  tone: Tone;
  label: string;
  // Why, when it is not plainly healthy.
  detail?: string;
}

function health(r: { ready?: string; readyMessage?: string; suspend?: boolean }): Health {
  if (r.suspend) return { tone: "amber", label: "Suspended", detail: "Reconciliation is paused (spec.suspend)." };
  if (!r.ready) return { tone: "neutral", label: "Waiting for first check", detail: "The controller has not reported yet." };
  if (r.ready === "True") return { tone: "green", label: "Healthy" };
  return { tone: "red", label: "Not ready", detail: r.readyMessage || "The controller reports this resource is not ready." };
}

const DOT: Record<Tone, string> = {
  green: "bg-turf",
  amber: "bg-amber",
  red: "bg-red",
  neutral: "bg-faint",
};
const TEXT: Record<Tone, string> = {
  green: "text-ink",
  amber: "text-amber",
  red: "text-red",
  neutral: "text-muted",
};

function StatusBadge({ h }: { h: Health }) {
  return (
    <span class={`inline-flex items-center gap-2 text-[13.5px] font-semibold whitespace-nowrap ${TEXT[h.tone]}`}>
      <span class={`size-2 rounded-full ${DOT[h.tone]}`} aria-hidden="true" />
      {h.label}
    </span>
  );
}

interface Attention {
  section: ConfigSection;
  anchor: string;
  name: string;
  kind: string;
  tone: Tone;
  message: string;
}

function attentionItems(config: ConfigDataset): Attention[] {
  const out: Attention[] = [];
  for (const i of config.integrations) {
    const h = health(i);
    if (h.tone === "red" || h.tone === "amber") {
      out.push({ section: "integrations", anchor: anchorFor("integration", i.name), name: i.name, kind: "Integration", tone: h.tone, message: h.detail ?? h.label });
    }
    if (i.redelivery?.error) {
      out.push({ section: "integrations", anchor: anchorFor("integration", i.name), name: i.name, kind: "Redelivery sweep", tone: "red", message: i.redelivery.error });
    }
    if (i.backfill?.error) {
      out.push({ section: "integrations", anchor: anchorFor("integration", i.name), name: i.name, kind: "Backfill", tone: "red", message: i.backfill.error });
    }
  }
  for (const f of config.forges) {
    const h = health(f);
    if (h.tone === "red" || h.tone === "amber") {
      out.push({ section: "forges", anchor: anchorFor("forge", f.name), name: f.name, kind: "Forge", tone: h.tone, message: h.detail ?? h.label });
    }
  }
  const conflicted = new Set(config.enhancers.filter((e) => e.ambiguous).map((e) => e.id));
  for (const id of conflicted) {
    out.push({
      section: "enhancers",
      anchor: "config-enhancers",
      name: ENHANCERS[id] ?? id,
      kind: "Enhancer",
      tone: "red",
      message: "Enabled on more than one integration. Context lookups stay off until exactly one enables it.",
    });
  }
  return out;
}

const anchorFor = (kind: string, name: string) => `config-${kind}-${name}`;

// jumpTo scrolls a section or card into view and records it in the URL
// without a hashchange (the hash is the router; the view is already open).
function jumpTo(e: MouseEvent, section: ConfigSection, anchor: string) {
  e.preventDefault();
  history.replaceState(null, "", hrefForConfig(section));
  const el = document.getElementById(anchor);
  el?.scrollIntoView({ behavior: "smooth", block: "start" });
  el?.focus({ preventScroll: true });
}

// ---- shared pieces --------------------------------------------------------------

function ProviderMark({ provider }: { provider: string }) {
  return (
    <span
      class="grid size-10 flex-none place-items-center rounded-lg border border-line bg-surface-2 font-mono text-[12.5px] font-bold text-fg"
      aria-hidden="true"
    >
      {provider === "github" ? <Icon name="github" size={20} /> : (MONOGRAMS[provider] ?? provider.slice(0, 2).toUpperCase())}
    </span>
  );
}

function CopyValue({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <span class="inline-flex items-center gap-1.5">
      <code class="font-mono text-[13px] break-all text-fg">{value}</code>
      <button
        type="button"
        class="grid size-7 flex-none cursor-pointer place-items-center rounded-md border-0 bg-transparent text-muted hover:bg-surface-2 hover:text-fg"
        aria-label={`Copy ${value}`}
        onClick={() => {
          void navigator.clipboard?.writeText(value).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1600);
          });
        }}
      >
        <Icon name={copied ? "check" : "copy"} size={14} />
      </button>
      <span class="sr-only" aria-live="polite">
        {copied ? "Copied" : ""}
      </span>
    </span>
  );
}

function Fact({ label, children }: { label: string; children: ComponentChildren }) {
  return (
    <div class="min-w-0">
      <dt class="text-[12.5px] text-muted">{label}</dt>
      <dd class="m-0 mt-1 text-[14px] text-fg">{children}</dd>
    </div>
  );
}

function Chips({ items, mono = false }: { items: string[]; mono?: boolean }) {
  return (
    <ul class="m-0 flex list-none flex-wrap gap-1.5 p-0">
      {items.map((c) => (
        <li
          key={c}
          class={`rounded-md border border-line bg-surface-2 px-2 py-0.5 text-[12.5px] text-fg ${mono ? "font-mono" : ""}`}
        >
          {c}
        </li>
      ))}
    </ul>
  );
}

function Callout({ tone, children }: { tone: "red" | "amber"; children: ComponentChildren }) {
  return (
    <div
      class={`flex items-start gap-2.5 rounded-lg border px-3.5 py-2.5 text-[13.5px] ${
        tone === "red"
          ? "border-[color-mix(in_oklab,var(--patchy-red)_35%,transparent)] bg-[color-mix(in_oklab,var(--patchy-red)_7%,transparent)] text-red"
          : "border-[color-mix(in_oklab,var(--patchy-amber)_35%,transparent)] bg-[color-mix(in_oklab,var(--patchy-amber)_7%,transparent)] text-amber"
      }`}
    >
      <Icon name="alertTriangle" size={15} class="mt-0.5 flex-none" />
      <div class="min-w-0 [overflow-wrap:anywhere]">{children}</div>
    </div>
  );
}

const CARD = "ps-card scroll-mt-24 outline-none";

function CardHead({ provider, name, subtitle, h }: { provider: string; name: string; subtitle: string; h: Health }) {
  return (
    <header class="flex items-start gap-3.5">
      <ProviderMark provider={provider} />
      <div class="min-w-0 flex-1">
        <h3 class="m-0 font-mono text-[16px] font-bold [overflow-wrap:anywhere] text-fg">{name}</h3>
        <p class="m-0 mt-0.5 text-[13.5px] text-muted">{subtitle}</p>
      </div>
      <StatusBadge h={h} />
    </header>
  );
}

// ---- integrations --------------------------------------------------------------

// BackfillTrigger is the two-click backfill request: an optional
// comma/space-separated repository prefix filter and a confirm-on-second-
// click button.
function BackfillTrigger({
  integration,
  busy,
  onBackfill,
}: {
  integration: IntegrationConfig;
  busy: boolean;
  onBackfill: (repos: string[]) => void;
}) {
  const [filter, setFilter] = useState("");
  const repos = filter
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
  const pending = integration.backfill?.pending;
  const inputId = `backfill-${integration.name}`;

  return (
    <div class="border-t border-line px-5 py-4">
      <label for={inputId} class="block text-[14px] font-bold text-fg">
        Backfill open alerts
      </label>
      <p class="m-0 mt-0.5 mb-3 max-w-[72ch] text-[13px] text-muted">
        Lists the open alerts webhooks may have missed and ingests them as findings. Limit it to repositories that start
        with a prefix, or leave it empty for everything this integration can see.
      </p>
      <div class="flex flex-wrap items-center gap-2">
        <input
          id={inputId}
          type="text"
          class="ps-input min-w-[240px] flex-1 font-mono text-[13px]"
          placeholder="acme/  or  acme/shop, acme/api"
          value={filter}
          disabled={busy || pending}
          spellcheck={false}
          onInput={(e) => {
            setFilter((e.target as HTMLInputElement).value);
          }}
        />
        <BackfillButton
          busy={busy}
          pending={pending}
          resetKey={filter}
          confirmLabel={
            repos.length
              ? `Confirm: backfill ${repos.length} prefix${repos.length > 1 ? "es" : ""}`
              : "Confirm: backfill everything"
          }
          onConfirm={() => onBackfill(repos)}
        />
      </div>
    </div>
  );
}

function Activity({ integration }: { integration: IntegrationConfig }) {
  const red = integration.redelivery;
  const bf = integration.backfill;
  const lines: ComponentChildren[] = [];
  if (red?.lastSweepAt) {
    lines.push(
      <li key="red">
        <span class="font-semibold">Redelivery sweep</span>{" "}
        <span class="text-muted">
          {formatAgo(red.lastSweepAt)}: checked {formatCount(red.scanned)} deliveries, redelivered{" "}
          {formatCount(red.redelivered)}.{red.truncated ? " Stopped at its page budget." : ""}
        </span>
        {red.error ? <span class="block text-red">{red.error}</span> : null}
      </li>,
    );
  }
  if (bf?.pending) {
    lines.push(
      <li key="bf-pending">
        <span class="font-semibold">Backfill requested</span>{" "}
        <span class="text-muted">
          {bf.requestedAt ? `${formatAgo(bf.requestedAt)} ` : ""}by {bf.requestedBy || "someone"}. Waiting for the
          controller to pick it up.
        </span>
      </li>,
    );
  }
  if (bf?.lastRunAt) {
    lines.push(
      <li key="bf">
        <span class="font-semibold">Last backfill</span>{" "}
        <span class="text-muted">
          {formatAgo(bf.lastRunAt)}
          {bf.requestedBy && !bf.pending ? ` by ${bf.requestedBy}` : ""}: listed {formatCount(bf.listed)} open alert
          {bf.listed === 1 ? "" : "s"}, ingested {formatCount(bf.ingested)}.
        </span>
        {bf.truncated ? (
          <span class="block text-amber">
            It stopped at its page budget before covering everything. Run it again with a narrower prefix to reach the
            rest.
          </span>
        ) : null}
        {bf.error ? <span class="block text-red">{bf.error}</span> : null}
      </li>,
    );
  }
  if (lines.length === 0) return null;
  return (
    <div class="border-t border-line px-5 py-4">
      <h4 class="m-0 mb-2 text-[13px] font-semibold text-muted">Recent activity</h4>
      <ul class="m-0 flex list-none flex-col gap-1.5 p-0 text-[13.5px]">{lines}</ul>
    </div>
  );
}

function IntegrationCard({
  integration: i,
  canBackfill,
  busy,
  onBackfill,
}: {
  integration: IntegrationConfig;
  canBackfill: boolean;
  busy: boolean;
  onBackfill: (repos: string[]) => void;
}) {
  const h = health(i);
  const caps = (i.capabilities ?? []).map((c) => capabilityLabel(i.provider, c));
  const interval = humanInterval(i.interval);
  return (
    <article id={anchorFor("integration", i.name)} tabIndex={-1} class={CARD} aria-label={`Integration ${i.name}`}>
      <div class="flex flex-col gap-4 px-5 py-4.5">
        <CardHead provider={i.provider} name={i.name} subtitle={PROVIDERS[i.provider] ?? i.provider} h={h} />
        {h.tone === "red" || h.tone === "amber" ? <Callout tone={h.tone}>{h.detail}</Callout> : null}
        {caps.length > 0 ? <Chips items={caps} /> : <p class="m-0 text-[13.5px] text-muted">No capabilities enabled.</p>}
        <dl class="m-0 grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-x-6 gap-y-3">
          {i.webhookPath ? (
            <Fact label="Webhook path">
              <CopyValue value={i.webhookPath} />
            </Fact>
          ) : null}
          <Fact label="Credentials secret">
            {i.secretRef ? <code class="font-mono text-[13px]">{i.secretRef}</code> : <span class="text-muted">None</span>}
          </Fact>
          {interval ? <Fact label="Checks">{interval}</Fact> : null}
        </dl>
      </div>
      <Activity integration={i} />
      {canBackfill && i.backfillSupported && !i.suspend ? (
        <BackfillTrigger integration={i} busy={busy} onBackfill={onBackfill} />
      ) : null}
    </article>
  );
}

// ---- forges ----------------------------------------------------------------------

function forgeHost(f: ForgeConfig): string {
  if (!f.baseURL) return "github.com";
  try {
    return new URL(f.baseURL).host;
  } catch {
    return f.baseURL;
  }
}

function ForgeCard({ forge: f }: { forge: ForgeConfig }) {
  const h = health(f);
  const interval = humanInterval(f.interval);
  const host = forgeHost(f);
  return (
    <article id={anchorFor("forge", f.name)} tabIndex={-1} class={CARD} aria-label={`Forge ${f.name}`}>
      <div class="flex flex-col gap-4 px-5 py-4.5">
        <CardHead provider={f.provider} name={f.name} subtitle={`${PROVIDERS[f.provider] ?? f.provider} at ${host}`} h={h} />
        {h.tone === "red" || h.tone === "amber" ? <Callout tone={h.tone}>{h.detail}</Callout> : null}
        <dl class="m-0 grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-x-6 gap-y-3">
          <Fact label="Organisations">
            {f.orgs?.length ? <Chips items={f.orgs} mono /> : <span>Any on {host}</span>}
          </Fact>
          <Fact label="Repositories">
            {f.repositories?.length ? <Chips items={f.repositories} mono /> : <span>All in those organisations</span>}
          </Fact>
          <Fact label="Credentials secret">
            {f.secretRef ? <code class="font-mono text-[13px]">{f.secretRef}</code> : <span class="text-muted">None</span>}
          </Fact>
          {interval ? <Fact label="Checks">{interval}</Fact> : null}
        </dl>
      </div>
    </article>
  );
}

// ---- enhancers ---------------------------------------------------------------------

function EnhancersTable({ enhancers, onJump }: { enhancers: EnhancerConfig[]; onJump: (e: MouseEvent, name: string) => void }) {
  return (
    <div class={`${CARD} overflow-x-auto`}>
      <table class="ps-table min-w-[560px]">
        <thead>
          <tr>
            <th scope="col">Enhancer</th>
            <th scope="col">Configured on</th>
            <th scope="col">Status</th>
          </tr>
        </thead>
        <tbody>
          {enhancers.map((e) => {
            const h: Health = e.ambiguous
              ? { tone: "red", label: "Conflict" }
              : e.enabled
                ? { tone: "green", label: "On" }
                : { tone: "neutral", label: "Off" };
            return (
              <tr key={`${e.id}/${e.integration}`}>
                <th scope="row" class="font-normal">
                  <span class="block font-semibold text-fg">{ENHANCERS[e.id] ?? e.id}</span>
                  {e.ambiguous ? (
                    <span class="block text-[13px] text-red">
                      Enabled on more than one integration. Turn it off on all but one.
                    </span>
                  ) : null}
                </th>
                <td>
                  {e.integration ? (
                    <a
                      class="font-mono text-[13px] text-fg underline decoration-line-3 underline-offset-2 hover:text-ink"
                      href={hrefForConfig("integrations")}
                      onClick={(ev) => onJump(ev, e.integration!)}
                    >
                      {e.integration}
                    </a>
                  ) : (
                    <span class="text-muted">Controller flag</span>
                  )}
                </td>
                <td>
                  <StatusBadge h={h} />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// ---- overview ----------------------------------------------------------------------

interface Stage {
  section: ConfigSection;
  anchor: string;
  title: string;
  count: string;
  members: { name: string; h: Health }[];
  empty: string;
}

function Overview({ config }: { config: ConfigDataset }) {
  const sources = config.integrations.filter(isSource);
  const stages: Stage[] = [
    {
      section: "integrations",
      anchor: "config-integrations",
      title: "Findings arrive",
      count: `${sources.length} source${sources.length === 1 ? "" : "s"}`,
      members: sources.map((i) => ({ name: i.name, h: health(i) })),
      empty: "No integration brings findings in yet.",
    },
    {
      section: "enhancers",
      anchor: "config-enhancers",
      title: "Context is added",
      count: `${config.enhancers.length} enhancer${config.enhancers.length === 1 ? "" : "s"}`,
      members: config.enhancers.map((e) => ({
        name: ENHANCERS[e.id] ?? e.id,
        h: e.ambiguous ? { tone: "red", label: "Conflict" } : { tone: e.enabled ? "green" : "neutral", label: e.enabled ? "On" : "Off" },
      })),
      empty: "Optional. Findings are investigated without extra context.",
    },
    {
      section: "forges",
      anchor: "config-forges",
      title: "Code is fetched and changed",
      count: `${config.forges.length} forge${config.forges.length === 1 ? "" : "s"}`,
      members: config.forges.map((f) => ({ name: f.name, h: health(f) })),
      empty: "No forge yet, so no repository can be investigated.",
    },
  ];

  const issues = attentionItems(config);
  const all = [...config.integrations.map(health), ...config.forges.map(health)];
  const waiting = all.filter((h) => h.tone === "neutral").length;
  const total = all.length;

  let summary: ComponentChildren;
  if (issues.length > 0) {
    summary = (
      <>
        {issues.length === 1 ? "One thing needs" : `${issues.length} things need`} attention.
      </>
    );
  } else if (total === 0) {
    summary = "Nothing is connected yet.";
  } else if (waiting > 0) {
    summary = `${total - waiting} of ${total} connections are healthy; ${waiting} ${waiting === 1 ? "is" : "are"} waiting for a first check.`;
  } else {
    summary = total === 1 ? "The connection is healthy." : `All ${total} connections are healthy.`;
  }

  return (
    <section class="ps-card" aria-labelledby="config-summary">
      <div class="flex items-start gap-3 px-6 pt-5.5">
        <span
          class={`mt-2 size-2.5 flex-none rounded-full ${issues.length ? "bg-red" : waiting || total === 0 ? "bg-faint" : "bg-turf"}`}
          aria-hidden="true"
        />
        <p id="config-summary" class="m-0 text-[21px] leading-snug font-bold text-fg">
          {summary}
        </p>
      </div>
      {issues.length > 0 ? (
        <ul class="m-0 mt-3 flex list-none flex-col gap-2 px-6 pl-[2.9rem]">
          {issues.map((a, n) => (
            <li key={n} class="text-[14px]">
              <a
                href={hrefForConfig(a.section)}
                onClick={(e) => jumpTo(e, a.section, a.anchor)}
                class={`font-semibold underline underline-offset-2 ${TEXT[a.tone]}`}
              >
                {a.kind} <span class="font-mono">{a.name}</span>
              </a>
              <span class="text-muted">: {a.message}</span>
            </li>
          ))}
        </ul>
      ) : null}
      <ol class="m-0 mt-5 grid list-none grid-cols-3 border-t border-line p-0 max-lg:grid-cols-1">
        {stages.map((s, n) => (
          <li key={s.section} class="relative border-l border-line first:border-l-0 max-lg:border-t max-lg:border-l-0 max-lg:first:border-t-0">
            <a
              href={hrefForConfig(s.section)}
              onClick={(e) => jumpTo(e, s.section, s.anchor)}
              class="flex h-full flex-col gap-2.5 px-6 py-4.5 text-inherit no-underline hover:bg-[color-mix(in_oklab,var(--patchy-green)_4%,var(--patchy-surface))]"
            >
              <span class="flex items-baseline justify-between gap-3">
                <span class="text-[15px] font-bold text-fg">{s.title}</span>
                <span class="text-[13px] text-muted">{s.count}</span>
              </span>
              {s.members.length > 0 ? (
                <ul class="m-0 flex list-none flex-col gap-1 p-0">
                  {s.members.map((m, k) => (
                    <li key={k} class="flex items-center gap-2 text-[13.5px]">
                      <span class={`size-2 flex-none rounded-full ${DOT[m.h.tone]}`} aria-hidden="true" />
                      <span class="truncate text-fg">{m.name}</span>
                      <span class="sr-only">: {m.h.label}</span>
                    </li>
                  ))}
                </ul>
              ) : (
                <span class="text-[13.5px] text-muted">{s.empty}</span>
              )}
            </a>
            {n < stages.length - 1 ? (
              <span
                class="absolute top-1/2 -right-3 z-[1] grid size-6 -translate-y-1/2 place-items-center rounded-full border border-line bg-surface text-muted max-lg:hidden"
                aria-hidden="true"
              >
                <Icon name="chevronDown" size={13} class="-rotate-90" />
              </span>
            ) : null}
          </li>
        ))}
      </ol>
    </section>
  );
}

// ---- page ----------------------------------------------------------------------------

function Section({
  id,
  title,
  count,
  intro,
  children,
}: {
  id: string;
  title: string;
  count: number;
  intro: string;
  children: ComponentChildren;
}) {
  return (
    <section id={id} tabIndex={-1} class="scroll-mt-24 outline-none" aria-labelledby={`${id}-title`}>
      <header class="mb-3">
        <h2 id={`${id}-title`} class="m-0 text-[19px] font-bold text-fg">
          {title} <span class="font-mono text-[14px] font-normal text-muted">{count}</span>
        </h2>
        <p class="m-0 mt-1 max-w-[72ch] text-[14px] text-muted">{intro}</p>
      </header>
      {children}
    </section>
  );
}

function Empty({ children }: { children: ComponentChildren }) {
  return (
    <p class="m-0 rounded-[10px] border border-dashed border-line-2 px-5 py-6 text-[14px] text-muted">{children}</p>
  );
}

const SECTION_ANCHORS: Record<ConfigSection, string> = {
  integrations: "config-integrations",
  enhancers: "config-enhancers",
  forges: "config-forges",
};

export function ConfigView({
  config,
  section,
  canBackfill,
  backfillBusy,
  onBackfill,
}: {
  config: ConfigDataset | null;
  section: ConfigSection;
  // Whether the server granted the backfill verb (user.integrationActions).
  canBackfill: boolean;
  // The integration a backfill request is in flight for, or null.
  backfillBusy: string | null;
  onBackfill: (integration: string, repos: string[]) => void;
}) {
  // Deep links (#/config/forges) land on their section once it renders.
  const loaded = config !== null;
  useEffect(() => {
    if (!loaded || section === "integrations") return;
    document.getElementById(SECTION_ANCHORS[section])?.scrollIntoView({ block: "start" });
  }, [loaded, section]);

  if (!config) {
    return <div class="px-5 py-11 text-center text-muted">Loading…</div>;
  }

  return (
    <div class="flex flex-col gap-9">
      <div class="flex flex-col gap-5">
        <header>
          <h1 class="ps-title">Configuration</h1>
          <p class="mt-1 mb-0 max-w-[80ch] text-[14px] text-muted">
            How patchy connects to your scanners and code
            {config.namespace ? (
              <>
                {" "}
                in namespace <span class="font-mono text-fg">{config.namespace}</span>
              </>
            ) : null}
            . These resources are managed in the cluster, so this page is read-only
            {canBackfill ? " apart from backfill" : ""}.
          </p>
        </header>
        <Overview config={config} />
      </div>

      <Section
        id="config-integrations"
        title="Integrations"
        count={config.integrations.length}
        intro="Each one connects a scanner or cloud account. Most bring alerts in as findings; some also carry the settings for a context lookup or for tracking issues."
      >
        {config.integrations.length === 0 ? (
          <Empty>No integrations yet. Findings arrive once an Integration resource exists in this namespace.</Empty>
        ) : (
          <div class="flex flex-col gap-4">
            {config.integrations.map((i) => (
              <IntegrationCard
                key={i.name}
                integration={i}
                canBackfill={canBackfill}
                busy={backfillBusy === i.name}
                onBackfill={(repos) => onBackfill(i.name, repos)}
              />
            ))}
          </div>
        )}
      </Section>

      <Section
        id="config-enhancers"
        title="Context enhancers"
        count={config.enhancers.length}
        intro="Look up owners, labels and tags for each finding before it is investigated. Each is switched on by a setting on an integration."
      >
        {config.enhancers.length === 0 ? (
          <Empty>
            None switched on. Turn on Cloud Asset Inventory, resource tags or a generic lookup on an integration to add
            context.
          </Empty>
        ) : (
          <EnhancersTable
            enhancers={config.enhancers}
            onJump={(e, name) => jumpTo(e, "integrations", anchorFor("integration", name))}
          />
        )}
      </Section>

      <Section
        id="config-forges"
        title="Forges"
        count={config.forges.length}
        intro="Where patchy fetches code to investigate and pushes the changes it proposes. A repository uses the most specific forge that covers it."
      >
        {config.forges.length === 0 ? (
          <Empty>No forges yet. Add a Forge resource so patchy can read the repositories your findings point at.</Empty>
        ) : (
          <div class="grid grid-cols-2 gap-4 max-lg:grid-cols-1">
            {config.forges.map((f) => (
              <ForgeCard key={f.name} forge={f} />
            ))}
          </div>
        )}
      </Section>

      {config.generatedAt ? (
        <p class="m-0 text-[13px] text-faint">As of {formatDate(config.generatedAt)}. Updates live while this page is open.</p>
      ) : null}
    </div>
  );
}
