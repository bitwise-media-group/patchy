import type { Rollup, ScopeType } from "../types";
import { formatAgo, formatDate } from "../format";
import { hrefForRollups } from "../router";
import { RollupOverall } from "./RollupOverall";
import { RollupTable } from "./RollupTable";

const SCOPE_TABS: [ScopeType, string][] = [
  ["total", "Overall"],
  ["repository", "By repository"],
  ["harness", "By harness"],
  ["model", "By model"],
];

export function RollupsView({ rollups, scope }: { rollups: Rollup[]; scope: ScopeType }) {
  const total = rollups.find((r) => r.scope.type === "total");
  const scoped = rollups.filter((r) => r.scope.type === scope);
  const epoch = total ?? scoped[0];

  return (
    <div>
      <nav class="ps-tabs mb-5" aria-label="Rollup scopes">
        {SCOPE_TABS.map(([s, label]) => (
          <a key={s} href={hrefForRollups(s)} class={s === scope ? "is-active" : ""} aria-current={s === scope ? "page" : undefined}>
            {label}
          </a>
        ))}
      </nav>
      {scope === "total" ? (
        total ? (
          <RollupOverall rollup={total} />
        ) : (
          <div class="px-5 py-11 text-center text-muted">No total rollup yet — stats appear once findings complete.</div>
        )
      ) : (
        <RollupTable scope={scope} rollups={scoped} />
      )}
      {epoch?.firstProcessed ? (
        <p class="mt-5 font-mono text-[10.5px] text-faint">
          Stats since {formatDate(epoch.firstProcessed)}
          {epoch.lastProcessed ? ` · updated ${formatAgo(epoch.lastProcessed)}` : ""}. Rollups survive finding
          expiry; a cluster rebuild resets the epoch.
        </p>
      ) : null}
    </div>
  );
}
