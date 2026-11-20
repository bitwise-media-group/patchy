import type { Finding } from "../types";
import { DASH, formatConfidence, formatCount, formatDate, formatTokens, holdText } from "../format";
import { Icon } from "./icons";
import { Markdown } from "./Markdown";
import { Pill, VerdictPill } from "./Pills";
import { RunAccountingRows } from "./RunUsage";
import { Conversation } from "./Transcript";

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
            <RunAccountingRows harness={inv.harness} model={inv.model} usage={inv.usage} />
            {inv.estimate ? (
              <div>
                <dt>Estimated fix</dt>
                <dd>
                  <span class="font-mono">{formatCount(inv.estimate.maxTurns)}</span>
                  <span class="text-muted"> turns, </span>
                  <span class="font-mono">{formatTokens(inv.estimate.tokenBudget)}</span>
                  <span class="text-muted"> output tokens</span>
                  <small class="mt-0.5 block text-[12px] text-muted">
                    A prediction, not a limit. The remediation tab shows what the run was granted and spent.
                  </small>
                </dd>
              </div>
            ) : null}
          </dl>
        ) : (
          <p class="text-faint">No investigation run yet.</p>
        )}
        {inv?.holdReasons?.length ? (
          <div class="ps-note">
            <Icon name="alertTriangle" size={15} />
            <span class="min-w-0 flex-1">
              Held for approval: {inv.holdReasons.map(holdText).join("; ")}.
            </span>
          </div>
        ) : null}
        {finding.activeRun?.kind === "investigation" ? (
          <p class="mt-3 inline-flex items-center gap-1.5 font-mono text-[12px] text-ink">
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
      <Conversation finding={finding} kind="investigation" />

    </div>
  );
}
