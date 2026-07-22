import { useState } from "preact/hooks";
import type { Finding, Rating } from "../types";
import { DASH, formatConfidence } from "../format";
import { hrefForFinding } from "../router";
import { Icon } from "./icons";
import { Markdown } from "./Markdown";
import { Pill, RatingPill, VerdictPill } from "./Pills";

// Scanner help text often opens with a heading that repeats the finding
// title; drop it rather than render the title twice.
function trimTitleHeading(md: string, title?: string): string {
  const m = /^\s*#{1,6}\s+(.+?)\s*(?:\n|$)/.exec(md);
  return m && title && m[1].trim().toLowerCase() === title.trim().toLowerCase()
    ? md.slice(m[0].length)
    : md;
}

function AnalysisRow({
  title,
  rating,
  detail,
}: {
  title: string;
  rating?: Rating;
  detail: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div>
      <button
        type="button"
        class="flex w-full cursor-pointer items-center gap-3 bg-surface px-4 py-3 text-[13px] text-inherit hover:bg-surface-2"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
      >
        <span class="font-semibold">{title}</span>
        <RatingPill rating={rating} />
        <Icon name="chevronDown" size={14} class={`ml-auto text-faint transition-transform ${open ? "rotate-180" : ""}`} />
      </button>
      {open ? <p class="m-0 bg-surface px-4 pb-3.5 text-[12.5px] leading-relaxed text-muted">{detail}</p> : null}
    </div>
  );
}

export function OverviewTab({ finding }: { finding: Finding }) {
  const inv = finding.investigation;
  return (
    <div class="pt-5 pb-2">
      <h1 class="m-0 text-[21px] leading-tight font-semibold tracking-tight">{finding.title ?? finding.name}</h1>
      <div class="mt-2.5 flex flex-wrap items-center gap-4 text-[12px] text-muted">
        {finding.ruleID ? <span class="ps-mono-tag">{finding.ruleID}</span> : null}
        <span>
          Confidence: <strong class="font-mono text-fg">{formatConfidence(inv?.confidence)}</strong>
        </span>
        <span class="inline-flex items-center gap-1.5">
          Verdict: <VerdictPill verdict={inv?.recommendation} />
        </span>
        {finding.priority ? (
          <span>
            Priority: <strong class="font-mono text-fg">{finding.priority}</strong>
          </span>
        ) : null}
      </div>
      {finding.description ? (
        <div class="mt-3.5 max-w-[720px]">
          <Markdown source={trimTitleHeading(finding.description, finding.title)} />
        </div>
      ) : null}

      {inv?.awaitApproval && finding.phase === "AwaitingApproval" ? (
        <div class="ps-note">
          <Icon name="alertTriangle" size={15} />
          The proposed fix needs human approval before it is queued
          {inv.outcome ? ` (${inv.outcome})` : ""}. Approving moves this finding to the remediation queue.
        </div>
      ) : null}

      <section class="mt-6">
        <h2 class="ps-heading mb-3">Analysis</h2>
        {inv ? (
          <div class="divide-y divide-line overflow-hidden rounded-[11px] border border-line">
            <AnalysisRow
              title="Exploitability"
              rating={inv.exploitability}
              detail={`How feasible exploitation is as the code ships. Investigation attempt ${inv.attempt ?? 1} · outcome ${inv.outcome ?? DASH}.`}
            />
            <AnalysisRow
              title="Likelihood"
              rating={inv.likelihood}
              detail={`How likely exploitation is given exposure and reachability. Recommendation: ${inv.recommendation ?? DASH} at ${formatConfidence(inv.confidence)} confidence.`}
            />
            <AnalysisRow
              title="Impact"
              rating={inv.impact}
              detail={`Blast radius if exploited.${inv.awaitApproval ? " Flagged for human approval before remediation." : ""}`}
            />
          </div>
        ) : (
          <p class="text-faint">No investigation has completed yet.</p>
        )}
      </section>

      {finding.enrichments && finding.enrichments.length > 0 ? (
        <section class="mt-6">
          <h2 class="ps-heading mb-3">Context</h2>
          {finding.enrichments.map((e) => (
            <div class="mb-3" key={e.enhancer}>
              <span class="ps-mono-tag">{e.enhancer}</span>
              {e.attributes && Object.keys(e.attributes).length > 0 ? (
                <ul class="mt-2 mb-1 flex list-disc flex-col gap-1 pl-4.5 text-[12.5px] marker:text-faint">
                  {Object.entries(e.attributes)
                    .sort(([a], [b]) => a.localeCompare(b))
                    .map(([k, v]) => (
                      <li key={k}>
                        <span class="text-muted">{k}:</span> <span class="text-fg">{v}</span>
                      </li>
                    ))}
                </ul>
              ) : null}
              {e.markdown ? (
                <div class="mt-2 mb-1">
                  <Markdown source={e.markdown} />
                </div>
              ) : null}
              {e.owners && e.owners.length > 0 ? <span class="text-faint">owners: {e.owners.join(", ")}</span> : null}
            </div>
          ))}
        </section>
      ) : null}

      {finding.related && finding.related.length > 0 ? (
        <section class="mt-6">
          <h2 class="ps-heading mb-3">Related findings</h2>
          <ul class="m-0 flex list-none flex-col gap-2 p-0">
            {finding.related.map((r) => (
              <li class="flex items-center gap-2" key={`${r.relationship}-${r.name}`}>
                <Pill tone="soil">{r.relationship}</Pill>
                <a class="font-mono text-[12px] text-ink" href={hrefForFinding(r.name)}>
                  {r.name}
                </a>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  );
}
