// The configuration view: the namespace's Forges, Integrations, and derived
// context enhancers. Read-only except the backfill trigger, which lives in
// the integrations section and renders only when the server granted the
// backfill verb (user.integrationActions) — read-only users never see a
// disabled button. Sub-tabs follow the RollupsView idiom; the trigger's
// two-click confirm follows ActionBar's.

import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import type { ConfigDataset, ForgeConfig, EnhancerConfig, IntegrationConfig } from "../types";
import { DASH, formatAgo, formatCount, formatDate } from "../format";
import { hrefForConfig, type ConfigSection } from "../router";
import { Icon } from "./icons";

const SECTION_TABS: [ConfigSection, string][] = [
  ["integrations", "Integrations"],
  ["forges", "Forges"],
  ["enhancers", "Enhancers"],
];

function ReadyBadge({ ready, message }: { ready?: string; message?: string }) {
  if (!ready) {
    return <span class="ps-pill" title="The controller has not reported yet">pending</span>;
  }
  const ok = ready === "True";
  return (
    <span
      class={`ps-pill ${ok ? "ps-pill--seedling" : "ps-pill--red"}`}
      title={message || undefined}
    >
      {ok ? "ready" : "not ready"}
    </span>
  );
}

function MetaRow({ label, children }: { label: string; children: ComponentChildren }) {
  return (
    <div class="flex items-baseline gap-2 text-[12.5px]">
      <span class="w-[110px] flex-none font-mono text-[10.5px] text-faint">{label}</span>
      <span class="min-w-0 break-all text-fg">{children}</span>
    </div>
  );
}

function ForgeCard({ forge }: { forge: ForgeConfig }) {
  return (
    <section class="rounded-xl border border-line-2 bg-surface p-4 shadow-card">
      <header class="mb-3 flex flex-wrap items-center gap-2">
        <h2 class="m-0 font-mono text-[14px] tracking-tight">{forge.name}</h2>
        <span class="ps-pill">{forge.provider}</span>
        {forge.suspend ? <span class="ps-pill ps-pill--amber">suspended</span> : null}
        <ReadyBadge ready={forge.ready} message={forge.readyMessage} />
      </header>
      <div class="flex flex-col gap-1.5">
        <MetaRow label="host">{forge.baseURL || "github.com"}</MetaRow>
        <MetaRow label="orgs">{forge.orgs?.join(", ") || "all"}</MetaRow>
        <MetaRow label="repositories">{forge.repositories?.join(", ") || "all"}</MetaRow>
        <MetaRow label="secret">{forge.secretRef || DASH}</MetaRow>
        <MetaRow label="interval">{forge.interval || DASH}</MetaRow>
      </div>
      {forge.readyMessage ? (
        <p class="mx-0 mt-3 mb-0 text-[12px] text-red">{forge.readyMessage}</p>
      ) : null}
    </section>
  );
}

// BackfillTrigger is the two-click backfill request: an optional
// comma/space-separated prefix filter and a confirm-on-second-click button,
// the ActionBar idiom (no modals).
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
  const [confirming, setConfirming] = useState(false);
  const repos = filter
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
  const pending = integration.backfill?.pending;

  return (
    <div class="mt-3 flex flex-wrap items-center gap-2">
      <input
        type="text"
        class="min-w-[240px] flex-1 rounded-lg border border-line-2 bg-transparent px-2.5 py-1.5 font-mono text-[11.5px] text-fg placeholder:text-faint"
        placeholder='repository filter, e.g. "acme/" or "acme/shop" (empty = full scope)'
        value={filter}
        disabled={busy}
        onInput={(e) => {
          setFilter((e.target as HTMLInputElement).value);
          setConfirming(false);
        }}
      />
      <button
        type="button"
        class={`ps-action ps-action--primary ${confirming ? "is-confirming" : ""}`}
        disabled={busy || pending}
        onClick={() => {
          if (confirming) {
            setConfirming(false);
            onBackfill(repos);
          } else {
            setConfirming(true);
          }
        }}
      >
        <Icon name="rotateCcw" size={14} />
        {busy
          ? "Requesting…"
          : pending
            ? "Backfill pending…"
            : confirming
              ? repos.length
                ? `Confirm backfill of ${repos.length} filter${repos.length > 1 ? "s" : ""}?`
                : "Confirm full-scope backfill?"
              : "Backfill alerts"}
      </button>
      {confirming ? (
        <button
          type="button"
          class="cursor-pointer border-0 bg-transparent font-mono text-[11px] text-muted underline"
          onClick={() => setConfirming(false)}
        >
          cancel
        </button>
      ) : null}
    </div>
  );
}

function BackfillReport({ integration }: { integration: IntegrationConfig }) {
  const bf = integration.backfill;
  if (!bf) return null;
  return (
    <div class="mt-2 flex flex-col gap-1 text-[12px]">
      {bf.pending ? (
        <p class="m-0 text-amber">
          Backfill requested by {bf.requestedBy || "someone"}
          {bf.requestedAt ? ` ${formatAgo(bf.requestedAt)}` : ""} — waiting for the controller.
        </p>
      ) : null}
      {bf.lastRunAt ? (
        <p class="m-0 text-muted">
          Last backfill {formatAgo(bf.lastRunAt)}: listed {formatCount(bf.listed)} open alert
          {bf.listed === 1 ? "" : "s"}, ingested {formatCount(bf.ingested)}.
        </p>
      ) : null}
      {bf.truncated ? (
        <p class="m-0 text-amber">
          <Icon name="alertTriangle" size={12} class="mr-1 inline-block align-[-1px]" />
          The walk hit the page budget before covering the estate — re-run with a narrower
          repository filter to reach the rest.
        </p>
      ) : null}
      {bf.error ? <p class="m-0 text-red">{bf.error}</p> : null}
    </div>
  );
}

function IntegrationCard({
  integration,
  canBackfill,
  busy,
  onBackfill,
}: {
  integration: IntegrationConfig;
  canBackfill: boolean;
  busy: boolean;
  onBackfill: (repos: string[]) => void;
}) {
  const red = integration.redelivery;
  return (
    <section class="rounded-xl border border-line-2 bg-surface p-4 shadow-card">
      <header class="mb-3 flex flex-wrap items-center gap-2">
        <h2 class="m-0 font-mono text-[14px] tracking-tight">{integration.name}</h2>
        <span class="ps-pill">{integration.provider}</span>
        {integration.suspend ? <span class="ps-pill ps-pill--amber">suspended</span> : null}
        <ReadyBadge ready={integration.ready} message={integration.readyMessage} />
      </header>
      <div class="flex flex-col gap-1.5">
        <MetaRow label="webhook">{integration.webhookPath || DASH}</MetaRow>
        <MetaRow label="secret">{integration.secretRef || DASH}</MetaRow>
        <MetaRow label="interval">{integration.interval || DASH}</MetaRow>
        <MetaRow label="capabilities">
          {integration.capabilities?.length ? (
            <span class="inline-flex flex-wrap gap-1">
              {integration.capabilities.map((c) => (
                <span key={c} class="ps-pill">{c}</span>
              ))}
            </span>
          ) : (
            DASH
          )}
        </MetaRow>
        {red?.lastSweepAt ? (
          <MetaRow label="redelivery">
            swept {formatAgo(red.lastSweepAt)} · {formatCount(red.scanned)} scanned ·{" "}
            {formatCount(red.redelivered)} redelivered
            {red.truncated ? " · truncated" : ""}
            {red.error ? <span class="text-red"> · {red.error}</span> : null}
          </MetaRow>
        ) : null}
      </div>
      {integration.readyMessage ? (
        <p class="mx-0 mt-3 mb-0 text-[12px] text-red">{integration.readyMessage}</p>
      ) : null}
      <BackfillReport integration={integration} />
      {canBackfill && integration.backfillSupported && !integration.suspend ? (
        <BackfillTrigger integration={integration} busy={busy} onBackfill={onBackfill} />
      ) : null}
    </section>
  );
}

function EnhancersTable({ enhancers }: { enhancers: EnhancerConfig[] }) {
  if (enhancers.length === 0) {
    return (
      <div class="px-5 py-11 text-center text-muted">
        No context enhancers configured — enable one on an Integration (Cloud Asset Inventory,
        resource tags, or a generic enhancer endpoint).
      </div>
    );
  }
  return (
    <div class="flex flex-col gap-3">
      {enhancers.map((e) => (
        <section
          key={`${e.id}/${e.integration}`}
          class="flex flex-wrap items-center gap-2 rounded-xl border border-line-2 bg-surface px-4 py-3 shadow-card"
        >
          <span class="font-mono text-[13px]">{e.id}</span>
          {e.integration ? <span class="text-[12px] text-muted">via {e.integration}</span> : null}
          <span class={`ps-pill ${e.enabled ? "ps-pill--seedling" : ""}`}>
            {e.enabled ? "enabled" : "disabled"}
          </span>
          {e.ambiguous ? (
            <span
              class="ps-pill ps-pill--red"
              title="Several integrations enable this singleton capability; the enhancer chain refuses the configuration until exactly one remains."
            >
              ambiguous
            </span>
          ) : null}
        </section>
      ))}
    </div>
  );
}

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
  if (!config) {
    return <div class="px-5 py-11 text-center text-muted">Loading…</div>;
  }

  let body;
  if (section === "forges") {
    body =
      config.forges.length === 0 ? (
        <div class="px-5 py-11 text-center text-muted">No forges configured.</div>
      ) : (
        <div class="flex flex-col gap-3">
          {config.forges.map((f) => (
            <ForgeCard key={f.name} forge={f} />
          ))}
        </div>
      );
  } else if (section === "enhancers") {
    body = <EnhancersTable enhancers={config.enhancers} />;
  } else {
    body =
      config.integrations.length === 0 ? (
        <div class="px-5 py-11 text-center text-muted">No integrations configured.</div>
      ) : (
        <div class="flex flex-col gap-3">
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
      );
  }

  return (
    <div>
      <nav class="ps-tabs mb-5" aria-label="Configuration sections">
        {SECTION_TABS.map(([s, label]) => (
          <a
            key={s}
            href={hrefForConfig(s)}
            class={s === section ? "is-active" : ""}
            aria-current={s === section ? "page" : undefined}
          >
            {label}
          </a>
        ))}
      </nav>
      {body}
      {config.generatedAt ? (
        <p class="mt-5 font-mono text-[10.5px] text-faint">
          Configuration as of {formatDate(config.generatedAt)}. This view is read-only; the
          resources are managed in the cluster (GitOps or kubectl).
        </p>
      ) : null}
    </div>
  );
}
