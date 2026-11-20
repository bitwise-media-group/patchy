// The one filter control every list page uses: a search field holding the
// active filters as removable chips, and a grouped list of suggestions that
// follows what is typed. Pages supply the chips and a suggest() function;
// this component owns the interaction.
//
// ARIA 1.2 combobox: focus stays in the input, arrow keys move
// aria-activedescendant through a grouped listbox, Enter picks, Escape
// closes then clears, Backspace in an empty field removes the last chip.
// With `shortcut`, "/" anywhere on the page focuses the field (one per page).

import type { ComponentChildren } from "preact";
import { useEffect, useId, useMemo, useRef, useState } from "preact/hooks";
import { Icon } from "./icons";

export interface FilterChip {
  id: string;
  kind: string; // what the chip filters on ("Severity", "Repository")
  value: string;
  mono?: boolean;
}

export interface FilterOption {
  key: string;
  content: ComponentChildren;
  aside?: ComponentChildren;
  // Already applied: shows a check, and picking it again removes it.
  selected?: boolean;
  onPick: () => void;
}

export interface FilterGroup {
  label?: string;
  note?: string;
  options: FilterOption[];
  footer?: string;
}

export function Highlight({ text, at, len }: { text: string; at: number; len: number }) {
  if (at < 0 || len === 0) return <>{text}</>;
  return (
    <>
      {text.slice(0, at)}
      <mark class="ps-match">{text.slice(at, at + len)}</mark>
      {text.slice(at + len)}
    </>
  );
}

export function FilterBox({
  chips,
  onRemove,
  suggest,
  placeholder,
  label,
  empty,
  shortcut = false,
  onQuery,
}: {
  chips: FilterChip[];
  onRemove: (id: string) => void;
  // Must be stable (useCallback): it is re-run whenever its identity or the
  // typed term changes.
  suggest: (term: string) => FilterGroup[];
  placeholder: string;
  // Accessible name of the field ("Filter findings").
  label: string;
  empty: (term: string) => ComponentChildren;
  shortcut?: boolean;
  // Told the trimmed term as it changes — for pages whose suggestions come
  // from the server (they fetch, then a new suggest() identity re-renders).
  onQuery?: (term: string) => void;
}) {
  const uid = useId();
  const listId = `${uid}-list`;
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const term = query.trim();

  const groups = useMemo(() => suggest(term).filter((g) => g.options.length > 0), [suggest, term]);
  const flat = useMemo(() => groups.flatMap((g) => g.options), [groups]);
  const optionId = (i: number) => `${uid}-o${i}`;

  useEffect(() => setActive(0), [query]);
  useEffect(() => onQuery?.(term), [term, onQuery]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  useEffect(() => {
    if (!shortcut) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return;
      const t = e.target as HTMLElement;
      if (t.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName)) return;
      e.preventDefault();
      input.current?.focus();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [shortcut]);

  useEffect(() => {
    if (open && flat[active]) document.getElementById(optionId(active))?.scrollIntoView({ block: "nearest" });
    // optionId is derived from uid, which never changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, open, flat]);

  const pick = (o: FilterOption) => {
    o.onPick();
    setQuery("");
    setOpen(false);
  };

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) return setOpen(true);
      const d = e.key === "ArrowDown" ? 1 : -1;
      setActive((a) => (flat.length === 0 ? 0 : (a + d + flat.length) % flat.length));
    } else if (e.key === "Enter") {
      if (open && flat[active]) {
        e.preventDefault();
        pick(flat[active]);
      }
    } else if (e.key === "Escape") {
      if (open) setOpen(false);
      else setQuery("");
    } else if (e.key === "Backspace" && query === "" && chips.length > 0) {
      onRemove(chips[chips.length - 1].id);
    }
  };

  let index = -1;
  return (
    <div class="relative w-full" ref={root}>
      <div
        class="ps-input flex min-h-10 flex-wrap items-center gap-1.5 py-1 pr-2 focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-[var(--focus-ring)]"
        onClick={() => input.current?.focus()}
      >
        <Icon name="search" size={16} class="flex-none text-muted" />
        {chips.map((c) => (
          <span key={c.id} class="inline-flex max-w-full items-center gap-1.5 rounded-md bg-surface-2 py-0.5 pr-1 pl-2 text-[12.5px]">
            <span class="flex-none text-muted">{c.kind}</span>
            <span class={`truncate text-fg ${c.mono ? "font-mono" : "font-semibold"}`}>{c.value}</span>
            <button
              type="button"
              class="grid size-5 flex-none cursor-pointer place-items-center rounded border-0 bg-transparent text-muted hover:text-fg"
              aria-label={`Remove filter ${c.kind} ${c.value}`}
              onClick={(e) => {
                e.stopPropagation();
                onRemove(c.id);
              }}
            >
              <Icon name="x" size={13} />
            </button>
          </span>
        ))}
        <input
          ref={input}
          type="text"
          role="combobox"
          aria-label={label}
          aria-expanded={open}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={open && flat[active] ? optionId(active) : undefined}
          autoComplete="off"
          spellcheck={false}
          class="min-w-[8rem] flex-1 border-0 bg-transparent py-1 text-[14px] text-fg outline-none placeholder:text-faint"
          placeholder={chips.length ? "Add a filter" : placeholder}
          value={query}
          onFocus={() => setOpen(true)}
          onClick={() => setOpen(true)}
          onInput={(e) => {
            setQuery((e.target as HTMLInputElement).value);
            setOpen(true);
          }}
          onKeyDown={onKeyDown}
        />
        {shortcut ? (
          <kbd class="flex-none rounded border border-line-2 px-1.5 font-mono text-[11px] text-muted max-sm:hidden" aria-hidden="true">
            /
          </kbd>
        ) : null}
      </div>
      {open ? (
        <div
          id={listId}
          role="listbox"
          aria-label={label}
          class="absolute top-[calc(100%+6px)] right-0 left-0 z-40 max-h-[min(70vh,520px)] overflow-y-auto rounded-[10px] border border-line-2 bg-surface pb-1.5 shadow-strong"
        >
          {flat.length === 0 ? <p class="m-0 px-4 py-4 text-[13.5px] text-muted">{empty(term)}</p> : null}
          {groups.map((g, gi) => (
            <div key={gi} role="group" aria-label={g.label}>
              {g.label ? (
                <div class="flex items-baseline justify-between gap-3 px-3 pt-3 pb-1 text-[12.5px] font-semibold text-muted" role="presentation">
                  <span>{g.label}</span>
                  {g.note ? <span class="font-normal text-faint">{g.note}</span> : null}
                </div>
              ) : (
                <div class="pt-1.5" role="presentation" />
              )}
              {g.options.map((o) => {
                const i = ++index;
                return (
                  <div
                    key={o.key}
                    id={optionId(i)}
                    role="option"
                    aria-selected={i === active}
                    class={`mx-1 flex cursor-pointer items-center justify-between gap-4 rounded-md px-2.5 py-2 text-[13.5px] text-fg ${
                      i === active ? "bg-[color-mix(in_oklab,var(--patchy-fg)_8%,transparent)]" : ""
                    }`}
                    onMouseEnter={() => setActive(i)}
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => pick(o)}
                  >
                    <span class="flex min-w-0 items-center gap-2">
                      <span class="min-w-0 truncate">{o.content}</span>
                      {o.selected ? (
                        <>
                          <Icon name="check" size={14} class="flex-none text-ink" />
                          <span class="sr-only">(applied, pick to remove)</span>
                        </>
                      ) : null}
                    </span>
                    {o.aside ? <span class="flex-none font-mono text-[12px] text-muted">{o.aside}</span> : null}
                  </div>
                );
              })}
              {g.footer ? (
                <p class="m-0 px-3.5 pt-1 pb-1 text-[12.5px] text-faint" role="presentation">
                  {g.footer}
                </p>
              ) : null}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}
