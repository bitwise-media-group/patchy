// The remediation pull request rendered as an outbound link: the boxed card
// (remediation tab + detail sidebar) and the pill the findings list swaps in
// for the in-review phase pill.

import type { PullRequestStatus } from "../types";
import { PHASE_LABELS, formatDate } from "../format";
import { Icon } from "./icons";

export function PullRequestCard({ pr }: { pr: PullRequestStatus }) {
  return (
    <a
      class="inline-flex items-center gap-3 rounded-[11px] border border-line-2 bg-surface px-4 py-3 text-fg no-underline hover:border-turf"
      href={pr.url}
      target="_blank"
      rel="noreferrer"
    >
      <Icon name="gitPullRequest" size={17} />
      <span class="flex flex-col">
        <strong class="font-mono text-[13px]">#{pr.number}</strong>
        <small class="mt-0.5 text-[12px] text-muted">
          {pr.state ?? "open"}
          {pr.mergedAt ? ` · merged ${formatDate(pr.mergedAt)}` : ""}
        </small>
      </span>
      <Icon name="externalLink" size={13} />
    </a>
  );
}

// PullRequestPill sits inside the row-level link in the findings table, so it
// stops propagation to keep a PR click from also triggering row navigation.
export function PullRequestPill({ pr }: { pr: PullRequestStatus }) {
  return (
    <a
      class="ps-pill ps-pill--seedling no-underline"
      href={pr.url}
      target="_blank"
      rel="noreferrer"
      onClick={(e) => e.stopPropagation()}
      title={`pull request #${pr.number} · ${pr.state ?? "open"}`}
    >
      <Icon name="gitPullRequest" size={11} />
      {PHASE_LABELS.InReview} #{pr.number}
      <Icon name="externalLink" size={10} />
    </a>
  );
}
