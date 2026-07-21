import type { Finding } from "../types";
import { TERMINAL_PHASES } from "../types";

export function StatTiles({ findings }: { findings: Finding[] }) {
  const open = findings.filter((f) => f.phase && !TERMINAL_PHASES.has(f.phase)).length;
  const awaiting = findings.filter((f) => f.phase === "AwaitingApproval").length;
  const inFlight = findings.filter(
    (f) => f.phase === "Queued" || f.phase === "Remediating" || f.phase === "InReview",
  ).length;
  const remediated = findings.filter((f) => f.phase === "Remediated").length;

  const stats: [string, number][] = [
    ["open findings", open],
    ["awaiting approval", awaiting],
    ["queued / remediating / in review", inFlight],
    ["remediated", remediated],
  ];

  return (
    <section
      class="grid grid-cols-4 overflow-hidden rounded-xl border border-line-2 bg-surface max-lg:grid-cols-2 max-lg:[&>.ps-stat]:odd:border-l-0 max-lg:[&>.ps-stat:nth-child(n+3)]:border-t max-lg:[&>.ps-stat:nth-child(n+3)]:border-t-line"
      aria-label="Findings summary"
    >
      {stats.map(([label, value]) => (
        <div class="ps-stat" key={label}>
          <strong>{value}</strong>
          <span>{label}</span>
        </div>
      ))}
    </section>
  );
}
