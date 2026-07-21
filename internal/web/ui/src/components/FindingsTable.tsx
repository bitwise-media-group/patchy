import type { Finding } from "../types";
import { confidencePercent, formatAgo, formatConfidence } from "../format";
import { hrefForFinding } from "../router";
import { Icon } from "./icons";
import { PhasePill, SeverityPill, VerdictPill, Pill } from "./Pills";

function FindingRow({ finding }: { finding: Finding }) {
  const advisory = finding.advisories[0] ?? finding.ruleID ?? finding.name;
  return (
    <a class="ps-grid-board ps-hover-row min-h-[72px] px-4.5 py-3 text-inherit no-underline" href={hrefForFinding(finding.name)}>
      <div>
        <span class="inline-flex items-center gap-1 font-mono text-[9.5px] text-faint">
          {finding.repository?.name ? (
            <>
              <Icon name="github" size={11} /> {finding.repository.name} ·{" "}
            </>
          ) : null}
          {advisory}
        </span>
        <strong class="mt-1 block text-[12.5px] leading-snug font-semibold text-fg">
          {finding.title ?? finding.name}
        </strong>
      </div>
      <div>
        <SeverityPill severity={finding.severity} />
      </div>
      <div class="flex flex-col items-start gap-1">
        <PhasePill phase={finding.phase} />
        {finding.suspend ? <Pill tone="amber">suspended</Pill> : null}
        {finding.activeRun ? (
          <span class="inline-flex items-center gap-1.5 font-mono text-[10.5px] text-ink" title={`${finding.activeRun.kind} running`}>
            <span class="ps-live-dot" /> {finding.activeRun.kind}
          </span>
        ) : null}
      </div>
      <div>
        <strong class="font-mono text-[11px] text-fg">{formatConfidence(finding.investigation?.confidence)}</strong>
        <span class="ps-track">
          <span style={{ width: `${confidencePercent(finding.investigation?.confidence)}%` }} />
        </span>
      </div>
      <div>
        <VerdictPill verdict={finding.investigation?.recommendation} />
      </div>
      <div class="font-mono text-[10.5px] text-muted">{formatAgo(finding.firstObservedAt)}</div>
    </a>
  );
}

export function FindingsTable({ findings }: { findings: Finding[] }) {
  return (
    <section class="mt-3.5 overflow-hidden rounded-xl border border-line-2 bg-surface shadow-card max-lg:overflow-x-auto" aria-label="Findings">
      <div class="ps-grid-board ps-table-header" aria-hidden="true">
        <span>Finding</span>
        <span>Severity</span>
        <span>Status</span>
        <span>Confidence</span>
        <span>Verdict</span>
        <span>First seen</span>
      </div>
      {findings.length === 0 ? (
        <div class="px-5 py-11 text-center text-muted">No findings match the current filters.</div>
      ) : (
        findings.map((f) => <FindingRow key={f.name} finding={f} />)
      )}
    </section>
  );
}
