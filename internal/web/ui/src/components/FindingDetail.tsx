import type { ActionVerb, Finding } from "../types";
import type { TabId } from "../router";
import { hrefForList } from "../router";
import { formatDate } from "../format";
import { Icon } from "./icons";
import { ActionBar } from "./ActionBar";
import { DetailHeader } from "./DetailHeader";
import { AlertsTab } from "./AlertsTab";
import { OverviewTab } from "./OverviewTab";
import { RemediationTab } from "./RemediationTab";
import { SidebarMeta } from "./SidebarMeta";
import { Tabs } from "./Tabs";
import { TimelineTab } from "./TimelineTab";

export function MissingFinding({ name }: { name: string }) {
  return (
    <div class="mx-auto my-20 max-w-[460px] rounded-xl border border-line-2 bg-surface p-7 text-center shadow-card">
      <p class="m-0">
        Finding <span class="ps-mono-tag">{name}</span> is no longer present — completed findings expire on a TTL.
      </p>
      <a class="mt-3.5 inline-flex items-center gap-1.5 font-semibold text-ink no-underline" href={hrefForList()}>
        <Icon name="arrowLeft" size={14} /> Back to findings
      </a>
    </div>
  );
}

export function FindingDetail({
  finding,
  tab,
  demo,
  busy,
  onAction,
  onSimulate403,
}: {
  finding: Finding;
  tab: TabId;
  demo: boolean;
  busy: ActionVerb | null;
  onAction: (verb: ActionVerb) => void;
  onSimulate403: () => void;
}) {
  return (
    <div class="animate-rise">
      <DetailHeader finding={finding} demo={demo} onSimulate403={onSimulate403} />
      {finding.phase === "Remediated" && tab !== "remediation" ? (
        <div class="ps-note ps-note--green mt-3.5 px-3 py-2">
          <Icon name="shieldCheck" size={14} />
          Remediation complete
          {finding.pullRequest?.mergedAt
            ? ` — PR #${finding.pullRequest.number} merged ${formatDate(finding.pullRequest.mergedAt)}.`
            : "."}
          {finding.pullRequest?.url ? (
            <a href={finding.pullRequest.url} target="_blank" rel="noreferrer">
              View pull request <Icon name="externalLink" size={11} />
            </a>
          ) : null}
        </div>
      ) : null}
      <div class="mt-1 grid grid-cols-[minmax(0,1fr)_300px] gap-7 max-lg:grid-cols-1">
        <div class="min-w-0">
          <Tabs finding={finding} active={tab} />
          {tab === "overview" ? <OverviewTab finding={finding} /> : null}
          {tab === "alerts" ? <AlertsTab finding={finding} /> : null}
          {tab === "timeline" ? <TimelineTab finding={finding} /> : null}
          {tab === "remediation" ? <RemediationTab finding={finding} /> : null}
        </div>
        <SidebarMeta finding={finding} />
      </div>
      <ActionBar finding={finding} busy={busy} onAction={onAction} />
    </div>
  );
}
