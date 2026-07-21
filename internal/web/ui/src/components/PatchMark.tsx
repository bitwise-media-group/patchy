// The CSS patch mark — a square of new turf sliding over a soil hole, with
// two grass blades. Ported from the patchy marketing kit.

export function PatchMark({ size = 30 }: { size?: number }) {
  return (
    <span class="patch-mark" style={{ width: size, height: size }} aria-hidden="true">
      <span class="patch-mark__hole" />
      <span class="patch-mark__turf" />
      <span class="patch-mark__blade patch-mark__blade--one" />
      <span class="patch-mark__blade patch-mark__blade--two" />
    </span>
  );
}
