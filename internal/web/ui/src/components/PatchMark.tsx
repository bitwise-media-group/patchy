// The patchy mark: a 2×2 grid of work units with one tile (the patch)
// landing into its slot, nudged up and out so the motion reads. The grid is
// the system (repositories, stages, jobs); the filled tile is the change it
// makes. Drawn on a 24-unit grid so it stays crisp from favicon to header.
//
// index.html carries the same geometry as the favicon; keep the two in step.

export function PatchMark({ size = 26 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" class="flex-none">
      <g fill="none" stroke="var(--patchy-line-3)" stroke-width="1.6">
        <rect x="2.8" y="2.8" width="8.4" height="8.4" rx="2.2" />
        <rect x="2.8" y="12.8" width="8.4" height="8.4" rx="2.2" />
        <rect x="12.8" y="12.8" width="8.4" height="8.4" rx="2.2" />
      </g>
      {/* The empty slot the patch is leaving behind. */}
      <rect
        x="12.8"
        y="2.8"
        width="8.4"
        height="8.4"
        rx="2.2"
        fill="none"
        stroke="var(--patchy-line-3)"
        stroke-width="1.2"
        stroke-dasharray="2 2"
      />
      <rect x="14" y="1.6" width="9.2" height="9.2" rx="2.4" fill="var(--patchy-green)" transform="rotate(-6 18.6 6.2)" />
    </svg>
  );
}
