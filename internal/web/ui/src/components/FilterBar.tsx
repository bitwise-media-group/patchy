import type { Level, Recommendation } from "../types";
import type { Selection } from "../filters";
import { hasActiveFilters } from "../filters";
import { Combobox } from "./Combobox";

const SEVERITIES: Level[] = ["critical", "high", "medium", "low"];
const VERDICTS: Recommendation[] = ["remediate", "ignore", "manual"];

export function FilterBar({
  selection,
  repos,
  onChange,
}: {
  selection: Selection;
  repos: string[];
  onChange: (next: Selection) => void;
}) {
  const toggleSeverity = (s: Level) => {
    const severities = new Set(selection.severities);
    severities.has(s) ? severities.delete(s) : severities.add(s);
    onChange({ ...selection, severities });
  };
  const toggleVerdict = (v: Recommendation) => {
    const verdicts = new Set(selection.verdicts);
    verdicts.has(v) ? verdicts.delete(v) : verdicts.add(v);
    onChange({ ...selection, verdicts });
  };

  return (
    <div class="mt-4.5 flex flex-wrap items-center gap-2.5">
      <input
        type="search"
        class="min-h-9 flex-[1_1_220px] rounded-[9px] border border-line-2 bg-surface px-3 font-sans text-[13px] text-fg placeholder:text-faint"
        placeholder="Search title, advisory, repo, owner…"
        value={selection.search}
        onInput={(e) => onChange({ ...selection, search: (e.target as HTMLInputElement).value })}
        aria-label="Search findings"
      />
      <div class="ps-filter-group" role="group" aria-label="Severity">
        {SEVERITIES.map((s) => (
          <button
            key={s}
            type="button"
            class={selection.severities.has(s) ? "is-active" : ""}
            aria-pressed={selection.severities.has(s)}
            onClick={() => toggleSeverity(s)}
          >
            {s}
          </button>
        ))}
      </div>
      <div class="ps-filter-group" role="group" aria-label="Verdict">
        {VERDICTS.map((v) => (
          <button
            key={v}
            type="button"
            class={selection.verdicts.has(v) ? "is-active" : ""}
            aria-pressed={selection.verdicts.has(v)}
            onClick={() => toggleVerdict(v)}
          >
            {v}
          </button>
        ))}
      </div>
      <Combobox
        ariaLabel="Repository"
        value={selection.repo}
        options={[{ value: "", label: "all repositories" }, ...repos.map((r) => ({ value: r, label: r }))]}
        onChange={(repo) => onChange({ ...selection, repo })}
      />
      {hasActiveFilters(selection) ? (
        <button
          type="button"
          class="cursor-pointer border-0 bg-transparent font-mono text-[11px] text-ink underline"
          onClick={() =>
            onChange({ phases: new Set(), severities: new Set(), verdicts: new Set(), repo: "", search: "" })
          }
        >
          clear
        </button>
      ) : null}
    </div>
  );
}
