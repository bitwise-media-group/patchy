// Formatting helpers. Absent figures render as "—", never as false zeros.

import type { Phase, Usage } from "./types";

export const DASH = "—";

// PHASE_LABELS: mono-friendly lowercase labels matching the projected
// GitHub label vocabulary (security-finding: awaiting-approval, …).
export const PHASE_LABELS: Record<Phase, string> = {
  Opened: "opened",
  Enhanced: "enhanced",
  Investigating: "investigating",
  AwaitingApproval: "awaiting-approval",
  Queued: "queued",
  Remediating: "remediating",
  InReview: "in-review",
  Remediated: "remediated",
  Failed: "failed",
  Dismissed: "dismissed",
  HandedOff: "handed-off",
};

export function formatDate(iso?: string): string {
  if (!iso) return DASH;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return DASH;
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// formatAgo renders a compact relative time ("3h ago", "12d ago").
export function formatAgo(iso?: string, now = Date.now()): string {
  if (!iso) return DASH;
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return DASH;
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 60) return `${d}d ago`;
  return `${Math.round(d / 30)}mo ago`;
}

// formatConfidence renders the CRD's decimal string ("0.93") as "93%".
export function formatConfidence(confidence?: string): string {
  if (confidence === undefined || confidence === "") return DASH;
  const n = Number(confidence);
  if (Number.isNaN(n)) return DASH;
  return `${Math.round(n * 100)}%`;
}

export function confidencePercent(confidence?: string): number {
  const n = Number(confidence);
  return Number.isNaN(n) ? 0 : Math.max(0, Math.min(100, n * 100));
}

// formatMicroUSD renders exact micro-USD integers as dollars.
export function formatMicroUSD(micro?: number): string {
  if (micro === undefined) return DASH;
  const usd = micro / 1_000_000;
  if (usd >= 1000) {
    return `$${usd.toLocaleString(undefined, { maximumFractionDigits: 0 })}`;
  }
  return `$${usd.toFixed(2)}`;
}

// formatTokens abbreviates token sums ("1.2M", "84k").
export function formatTokens(tokens?: number): string {
  if (tokens === undefined) return DASH;
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1)}M`;
  if (tokens >= 1_000) return `${Math.round(tokens / 1_000)}k`;
  return String(tokens);
}

// totalTokens sums a usage block's token figures; undefined when nothing
// was reported (so it renders as a dash, not a false zero).
export function totalTokens(u?: Usage): number | undefined {
  if (!u) return undefined;
  const sum = (u.inputTokens ?? 0) + (u.outputTokens ?? 0) + (u.cacheReadTokens ?? 0) + (u.cacheCreationTokens ?? 0);
  return sum > 0 ? sum : undefined;
}

// usageBreakdown is the tooltip/detail split behind a summed token figure.
export function usageBreakdown(u?: Usage): string | undefined {
  if (!u || totalTokens(u) === undefined) return undefined;
  const cache = (u.cacheReadTokens ?? 0) + (u.cacheCreationTokens ?? 0);
  return `in ${formatTokens(u.inputTokens ?? 0)} · out ${formatTokens(u.outputTokens ?? 0)} · cache ${formatTokens(cache)}`;
}

// formatMs renders a millisecond sum or average as a compact duration.
export function formatMs(ms?: number): string {
  if (ms === undefined || Number.isNaN(ms)) return DASH;
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 90) return `${s.toFixed(s < 10 ? 1 : 0)}s`;
  const m = s / 60;
  if (m < 90) return `${m.toFixed(0)}m`;
  return `${(m / 60).toFixed(1)}h`;
}

// formatPercent renders a 0–1 ratio ("62%"); undefined (0/0) is a dash.
export function formatPercent(ratio?: number): string {
  if (ratio === undefined || Number.isNaN(ratio)) return DASH;
  return `${Math.round(ratio * 100)}%`;
}

// formatMonth renders a rollup month key ("2026-07") as "Jul 26".
export function formatMonth(key: string): string {
  const [y, m] = key.split("-").map(Number);
  if (!y || !m) return key;
  const d = new Date(Date.UTC(y, m - 1, 1));
  return d.toLocaleString(undefined, { month: "short", year: "2-digit", timeZone: "UTC" });
}

export function formatCount(n?: number): string {
  if (n === undefined) return DASH;
  return n.toLocaleString();
}

// skew is how far an actual figure ran over (positive) or under (negative)
// its prediction, as a 0-centred ratio. Undefined when there is nothing to
// compare against — a zero prediction has no meaningful percentage.
export function skew(predicted?: number, actual?: number): number | undefined {
  if (!predicted || actual === undefined) return undefined;
  return actual / predicted - 1;
}

// formatSkew renders a skew ratio with an explicit sign ("+183%", "-18%",
// "on target"). The sign is the point: it says which way the estimate was
// wrong, so it is never dropped for positive values.
export function formatSkew(ratio?: number): string {
  if (ratio === undefined || Number.isNaN(ratio)) return DASH;
  const pct = Math.round(ratio * 100);
  if (pct === 0) return "on target";
  return `${pct > 0 ? "+" : ""}${pct}%`;
}

// skewTone maps a skew onto a rendering tone. Small misses are unremarkable;
// the tone only escalates once an estimate is wrong enough to matter for
// scheduling or approval.
export function skewTone(ratio?: number): "neutral" | "amber" | "red" {
  if (ratio === undefined || Number.isNaN(ratio)) return "neutral";
  const m = Math.abs(ratio);
  if (m >= 1) return "red";
  if (m >= 0.25) return "amber";
  return "neutral";
}
