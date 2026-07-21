import { useEffect, useRef, useState } from "preact/hooks";
import type { Finding } from "../types";
import { hrefForList } from "../router";
import { Icon } from "./icons";
import { PhasePill, Pill, SeverityPill } from "./Pills";

export const ICON_BTN =
  "inline-grid size-8 cursor-pointer place-items-center rounded-lg border border-line-2 bg-surface text-muted no-underline hover:text-fg";

const MENU_ITEM =
  "flex cursor-pointer items-center justify-between gap-2.5 rounded-[7px] border-0 bg-transparent px-2.5 py-2 text-left text-[12.5px] text-fg no-underline hover:bg-surface-2";

function KebabMenu({ finding, demo, onSimulate403 }: { finding: Finding; demo: boolean; onSimulate403: () => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const copyLink = async () => {
    try {
      await navigator.clipboard.writeText(window.location.href);
    } catch {
      // Clipboard may be unavailable over file:// — nothing to recover.
    }
    setOpen(false);
  };

  const item = (label: string, href?: string, onClick?: () => void) =>
    href ? (
      <a class={MENU_ITEM} href={href} target="_blank" rel="noreferrer" role="menuitem" onClick={() => setOpen(false)}>
        {label} <Icon name="externalLink" size={11} />
      </a>
    ) : (
      <button class={MENU_ITEM} type="button" role="menuitem" onClick={onClick}>
        {label}
      </button>
    );

  return (
    <div class="relative" ref={ref}>
      <button
        type="button"
        class={ICON_BTN}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="More actions"
        onClick={() => setOpen(!open)}
      >
        <Icon name="moreVertical" size={16} />
      </button>
      {open ? (
        <div
          class="absolute top-[calc(100%+6px)] right-0 z-40 flex min-w-[210px] flex-col rounded-[10px] border border-line-2 bg-surface p-1 shadow-strong"
          role="menu"
        >
          {item("Copy link", undefined, copyLink)}
          {finding.tracking?.url ? item(`Tracking issue #${finding.tracking.issueNumber}`, finding.tracking.url) : null}
          {finding.pullRequest?.url ? item(`Pull request #${finding.pullRequest.number}`, finding.pullRequest.url) : null}
          {finding.repository?.url ? item("Repository", finding.repository.url) : null}
          {demo
            ? item("Simulate 403", undefined, () => {
                setOpen(false);
                onSimulate403();
              })
            : null}
        </div>
      ) : null}
    </div>
  );
}

export function DetailHeader({
  finding,
  demo,
  onSimulate403,
}: {
  finding: Finding;
  demo: boolean;
  onSimulate403: () => void;
}) {
  const [primary, ...rest] = finding.advisories.length > 0 ? finding.advisories : [finding.name];
  return (
    <div class="flex flex-wrap items-center gap-3.5 border-b border-line pb-4">
      <div class="flex flex-wrap items-center gap-2">
        <span class="font-mono text-[19px] font-bold tracking-tight">{primary}</span>
        {rest.map((a) => (
          <span class="ps-chip" key={a}>
            {a}
          </span>
        ))}
        {finding.repository?.name ? (
          <a class="ps-chip text-fg" href={finding.repository.url} target="_blank" rel="noreferrer">
            <Icon name="github" size={12} /> {finding.repository.name}
          </a>
        ) : (
          <span class="ps-chip" title="This finding has no repository and cannot be auto-remediated.">
            no repository
          </span>
        )}
      </div>
      <div class="ml-auto flex flex-wrap items-center gap-2 max-sm:ml-0">
        <PhasePill phase={finding.phase} label="Status" />
        <SeverityPill severity={finding.severity} label="Severity" />
        {finding.suspend ? <Pill tone="amber">suspended</Pill> : null}
        <KebabMenu finding={finding} demo={demo} onSimulate403={onSimulate403} />
        <a class={ICON_BTN} href={hrefForList()} aria-label="Back to findings list">
          <Icon name="x" size={16} />
        </a>
      </div>
    </div>
  );
}
