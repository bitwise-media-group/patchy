// The finding's history: the append-only phaseTimes log, with approval and
// PR-merge events woven in at their timestamps.

import type { Finding, Phase } from "../types";
import { formatAgo, formatDate, PHASE_LABELS } from "../format";
import { Icon } from "./icons";

type Entry = {
  key: string;
  title: string;
  detail?: string;
  at: string;
  tone: "done" | "active" | "red" | "soil" | "amber" | "green";
};

const TERMINAL_TONE: Partial<Record<Phase, Entry["tone"]>> = {
  Remediated: "green",
  Failed: "red",
  Dismissed: "soil",
  HandedOff: "amber",
};

function entries(finding: Finding): Entry[] {
  const out: Entry[] = [];
  const times = finding.phaseTimes ?? [];
  times.forEach((pt, i) => {
    const last = i === times.length - 1;
    out.push({
      key: `phase-${i}`,
      title: PHASE_LABELS[pt.phase],
      at: pt.at,
      tone: last ? (TERMINAL_TONE[pt.phase] ?? "active") : "done",
    });
  });
  if (finding.approval) {
    out.push({
      key: "approval",
      title: "approved",
      detail: `by ${finding.approval.by}${finding.approval.note ? ` — ${finding.approval.note}` : ""}`,
      at: finding.approval.at,
      tone: "green",
    });
  }
  if (finding.pullRequest?.mergedAt) {
    out.push({
      key: "pr-merged",
      title: `PR #${finding.pullRequest.number} merged`,
      at: finding.pullRequest.mergedAt,
      tone: "green",
    });
  }
  return out.sort((a, b) => a.at.localeCompare(b.at));
}

export function TimelineTab({ finding }: { finding: Finding }) {
  const list = entries(finding);
  return (
    <div class="pt-5 pb-2">
      {list.length === 0 ? <p class="text-faint">No history recorded yet.</p> : null}
      <div class="rounded-[11px] border border-line bg-code p-4">
        {list.map((e) => (
          <div class={`ps-timeline__item ps-timeline__item--${e.tone}`} key={e.key}>
            <span class="ps-timeline__marker">
              {e.tone === "done" || e.tone === "green" ? <Icon name="check" size={11} strokeWidth={2.6} /> : null}
            </span>
            <span class="ps-timeline__body">
              <strong class="block font-mono text-[12px] font-semibold text-fg">{e.title}</strong>
              {e.detail ? <small class="mt-0.5 block text-[11px] text-muted">{e.detail}</small> : null}
              <small class="mt-0.5 block font-mono text-[10px] text-faint">
                {formatDate(e.at)} · {formatAgo(e.at)}
              </small>
            </span>
          </div>
        ))}
      </div>
      {finding.suspend ? (
        <p class="ps-note">
          <Icon name="pause" size={14} /> This finding is suspended — the pipeline will not advance until it is resumed.
        </p>
      ) : null}
    </div>
  );
}
