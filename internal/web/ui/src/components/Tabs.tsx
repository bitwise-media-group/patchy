import type { Finding } from "../types";
import { hrefForFinding, type TabId } from "../router";

export function Tabs({ finding, active }: { finding: Finding; active: TabId }) {
  const alertCount = (finding.alerts?.length ?? 0) + (finding.overflowAlerts ?? 0);
  const tabs: [TabId, string][] = [
    ["overview", "Overview"],
    ["alerts", alertCount > 0 ? `Alerts (${alertCount})` : "Alerts"],
    ["timeline", "Timeline"],
    ["investigation", "Investigation"],
    ["remediation", "Remediation"],
  ];
  return (
    <nav class="ps-tabs" aria-label="Finding sections">
      {tabs.map(([id, label]) => (
        <a key={id} href={hrefForFinding(finding.name, id)} class={id === active ? "is-active" : ""} aria-current={id === active ? "page" : undefined}>
          {label}
        </a>
      ))}
    </nav>
  );
}
