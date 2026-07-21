// A small brand-styled combobox: button trigger + filterable listbox
// popover. Replaces native <select> so the open dropdown renders with the
// theme instead of browser chrome. Deliberately hand-rolled — no component
// library — to keep the single-file embed dependency-free.

import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { Icon } from "./icons";

export interface ComboOption {
  value: string;
  label: string;
}

export function Combobox({
  value,
  options,
  onChange,
  ariaLabel,
  searchThreshold = 6,
}: {
  value: string;
  options: ComboOption[];
  onChange: (value: string) => void;
  ariaLabel: string;
  // The filter input appears only when the list is long enough to need it.
  searchThreshold?: number;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const ref = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  const current = options.find((o) => o.value === value) ?? options[0];
  const searchable = options.length >= searchThreshold;
  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return needle === "" ? options : options.filter((o) => o.label.toLowerCase().includes(needle));
  }, [options, query]);

  useEffect(() => {
    if (!open) return;
    setQuery("");
    setActive(Math.max(0, options.findIndex((o) => o.value === value)));
    searchRef.current?.focus();
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
    // Reset is tied to the open transition only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const select = (v: string) => {
    onChange(v);
    setOpen(false);
  };

  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      setOpen(false);
      return;
    }
    if (!open && (e.key === "ArrowDown" || e.key === "Enter" || e.key === " ")) {
      e.preventDefault();
      setOpen(true);
      return;
    }
    if (!open) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const delta = e.key === "ArrowDown" ? 1 : -1;
      setActive((a) => Math.min(visible.length - 1, Math.max(0, a + delta)));
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (visible[active]) select(visible[active].value);
    }
  };

  return (
    <div class="relative" ref={ref} onKeyDown={onKey}>
      <button
        type="button"
        class="flex min-h-9 min-w-[170px] cursor-pointer items-center justify-between gap-2 rounded-[9px] border border-line-2 bg-surface px-2.5 font-mono text-[11px] text-fg"
        role="combobox"
        aria-expanded={open}
        aria-haspopup="listbox"
        aria-label={ariaLabel}
        onClick={() => setOpen(!open)}
      >
        <span class="truncate">{current?.label ?? ""}</span>
        <Icon name="chevronDown" size={13} class={`flex-none text-faint transition-transform ${open ? "rotate-180" : ""}`} />
      </button>
      {open ? (
        <div class="absolute top-[calc(100%+6px)] right-0 z-40 flex max-h-72 w-max min-w-full flex-col overflow-hidden rounded-[10px] border border-line-2 bg-surface shadow-strong">
          {searchable ? (
            <input
              ref={searchRef}
              type="text"
              class="m-1 rounded-[7px] border border-line bg-code-2 px-2.5 py-1.5 font-mono text-[11px] text-fg placeholder:text-faint"
              placeholder="filter…"
              value={query}
              aria-label={`Filter ${ariaLabel}`}
              onInput={(e) => {
                setQuery((e.target as HTMLInputElement).value);
                setActive(0);
              }}
            />
          ) : null}
          <ul class="m-0 list-none overflow-y-auto p-1" role="listbox" aria-label={ariaLabel}>
            {visible.length === 0 ? <li class="px-2.5 py-2 font-mono text-[11px] text-faint">no matches</li> : null}
            {visible.map((o, i) => (
              <li key={o.value} role="option" aria-selected={o.value === value}>
                <button
                  type="button"
                  class={`flex w-full cursor-pointer items-center justify-between gap-3 rounded-[7px] border-0 px-2.5 py-2 text-left font-mono text-[11px] ${
                    i === active ? "bg-surface-2 text-fg" : "bg-transparent text-muted"
                  }`}
                  onMouseEnter={() => setActive(i)}
                  onClick={() => select(o.value)}
                >
                  <span class="truncate">{o.label}</span>
                  {o.value === value ? <Icon name="check" size={12} class="flex-none text-ink" /> : null}
                </button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
