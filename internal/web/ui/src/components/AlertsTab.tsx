import type { Finding } from "../types";
import { Icon } from "./icons";

export function AlertsTab({ finding }: { finding: Finding }) {
  const alerts = finding.alerts ?? [];
  return (
    <div class="pt-5 pb-2">
      {alerts.length === 0 ? <p class="text-faint">No alerts recorded on this finding.</p> : null}
      {alerts.map((alert) => (
        <article class="mb-3.5 rounded-[11px] border border-line bg-surface px-4 py-3" key={alert.id}>
          <header>
            {alert.url ? (
              <a href={alert.url} target="_blank" rel="noreferrer" class="ps-mono-tag">
                {alert.id} <Icon name="externalLink" size={11} />
              </a>
            ) : (
              <span class="ps-mono-tag">{alert.id}</span>
            )}
          </header>
          {(alert.locations ?? []).map((loc) => (
            <div class="mt-2.5" key={`${loc.path}:${loc.startLine ?? 0}`}>
              <span class="font-mono text-[11.5px] text-fg [overflow-wrap:anywhere]">
                {loc.path}
                {loc.startLine ? `:${loc.startLine}${loc.endLine && loc.endLine !== loc.startLine ? `–${loc.endLine}` : ""}` : ""}
              </span>
              {loc.snippet ? (
                <pre class="mt-1.5 mb-0 overflow-x-auto rounded-[9px] border border-line bg-code px-3 py-2.5 font-mono text-[11px] leading-relaxed text-fg">
                  {loc.snippet}
                </pre>
              ) : null}
            </div>
          ))}
        </article>
      ))}
      {finding.overflowAlerts ? (
        <p class="text-[12px] text-faint">
          +{finding.overflowAlerts} more alert{finding.overflowAlerts === 1 ? "" : "s"} folded into this finding (cap
          reached) — see the tracking issue for the full list.
        </p>
      ) : null}
    </div>
  );
}
