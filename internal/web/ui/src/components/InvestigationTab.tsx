import type { Finding } from "../types";
import { DASH, formatConfidence, formatDate } from "../format";
import { Markdown } from "./Markdown";
import { Pill, VerdictPill } from "./Pills";

export function InvestigationTab({ finding }: { finding: Finding }) {
  const inv = finding.investigation;
  return (
    <div class="pt-5 pb-2">
      <section>
        <h2 class="ps-heading mb-3">Investigation run</h2>
        {inv ? (
          <dl class="ps-kv">
            <div>
              <dt>Outcome</dt>
              <dd>
                {inv.outcome === undefined ? (
                  DASH
                ) : inv.outcome === "ok" ? (
                  <Pill tone="green">{inv.outcome}</Pill>
                ) : (
                  <Pill tone="red">{inv.outcome}</Pill>
                )}
              </dd>
            </div>
            <div>
              <dt>Verdict</dt>
              <dd>
                <VerdictPill verdict={inv.recommendation} />
              </dd>
            </div>
            <div>
              <dt>Confidence</dt>
              <dd>
                <span class="font-mono">{formatConfidence(inv.confidence)}</span>
              </dd>
            </div>
            <div>
              <dt>Attempt</dt>
              <dd>{inv.attempt ?? DASH}</dd>
            </div>
            <div>
              <dt>Completed</dt>
              <dd>{formatDate(inv.completedAt)}</dd>
            </div>
          </dl>
        ) : (
          <p class="text-faint">No investigation run yet.</p>
        )}
        {finding.activeRun?.kind === "investigation" ? (
          <p class="mt-3 inline-flex items-center gap-1.5 font-mono text-[10.5px] text-ink">
            <span class="ps-live-dot" /> investigation <span class="ps-mono-tag">{finding.activeRun.name}</span> is
            running now.
          </p>
        ) : null}
      </section>

      <section class="mt-6">
        <h2 class="ps-heading mb-3">Report</h2>
        {inv?.report ? (
          <div class="rounded-[11px] border border-line bg-code p-4">
            <Markdown source={inv.report} />
          </div>
        ) : (
          <p class="text-faint">No report recorded{inv ? " (the investigation may have expired)" : ""}.</p>
        )}
      </section>
    </div>
  );
}
