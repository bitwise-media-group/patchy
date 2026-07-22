import type { Finding } from "../types";
import { DASH, formatDate } from "../format";
import { Icon } from "./icons";
import { Markdown } from "./Markdown";
import { Pill } from "./Pills";
import { PullRequestCard } from "./PullRequest";

export function RemediationTab({ finding }: { finding: Finding }) {
  const rem = finding.remediation;
  const pr = finding.pullRequest;
  return (
    <div class="pt-5 pb-2">
      {finding.phase === "Remediated" ? (
        <div class="ps-note ps-note--green mt-0">
          <Icon name="shieldCheck" size={15} />
          Remediation complete
          {pr?.mergedAt ? ` — PR #${pr.number} merged ${formatDate(pr.mergedAt)}.` : "."}
          {pr?.url ? (
            <a href={pr.url} target="_blank" rel="noreferrer">
              View pull request <Icon name="externalLink" size={11} />
            </a>
          ) : null}
        </div>
      ) : null}

      <section class="mt-6 first:mt-0">
        <h2 class="ps-heading mb-3">Remediation run</h2>
        {rem ? (
          <dl class="ps-kv">
            <div>
              <dt>Outcome</dt>
              <dd>
                {rem.success === undefined ? (
                  rem.outcome ?? DASH
                ) : rem.success ? (
                  <Pill tone="green">{rem.outcome ?? "ok"}</Pill>
                ) : (
                  <Pill tone="red">{rem.outcome ?? "failed"}</Pill>
                )}
              </dd>
            </div>
            <div>
              <dt>Branch</dt>
              <dd>{rem.branch ? <span class="ps-mono-tag">{rem.branch}</span> : DASH}</dd>
            </div>
            <div>
              <dt>Attempt</dt>
              <dd>{rem.attempt ?? DASH}</dd>
            </div>
            <div>
              <dt>Completed</dt>
              <dd>{formatDate(rem.completedAt)}</dd>
            </div>
          </dl>
        ) : (
          <p class="text-faint">No remediation run yet.</p>
        )}
        {finding.activeRun ? (
          <p class="mt-3 inline-flex items-center gap-1.5 font-mono text-[10.5px] text-ink">
            <span class="ps-live-dot" /> {finding.activeRun.kind} <span class="ps-mono-tag">{finding.activeRun.name}</span> is
            running now.
          </p>
        ) : null}
      </section>

      <section class="mt-6">
        <h2 class="ps-heading mb-3">Report</h2>
        {rem?.report ? (
          <div class="rounded-[11px] border border-line bg-code p-4">
            <Markdown source={rem.report} />
          </div>
        ) : (
          <p class="text-faint">No report recorded{rem ? " (the remediation may have expired)" : ""}.</p>
        )}
      </section>

      <section class="mt-6">
        <h2 class="ps-heading mb-3">Pull request</h2>
        {pr ? (
          <PullRequestCard pr={pr} />
        ) : (
          <p class="text-faint">No pull request opened yet.</p>
        )}
      </section>

      <section class="mt-6">
        <h2 class="ps-heading mb-3">Attempts</h2>
        <dl class="ps-kv">
          <div>
            <dt>Investigation</dt>
            <dd>{finding.attempts?.investigation ?? 0}</dd>
          </div>
          <div>
            <dt>Remediation</dt>
            <dd>{finding.attempts?.remediation ?? 0}</dd>
          </div>
        </dl>
      </section>

      {finding.lastFailureReason ? (
        <div class="ps-note ps-note--red">
          <Icon name="alertTriangle" size={15} />
          {finding.lastFailureReason}
        </div>
      ) : null}
    </div>
  );
}
