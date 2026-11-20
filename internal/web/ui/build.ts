// Production build of the status page: ONE self-contained dist/index.html, with
// every script, stylesheet and asset inlined, which the status-server binary
// embeds behind the `withui` build tag (internal/web/embed_ui.go).
//
// It goes through the Bun.build API rather than `bun build` on the command
// line because Tailwind is a bundler plugin, and the CLI does not load plugins.
// `compile` with a browser target is what produces the single inlined file.

import { rmSync } from "node:fs";
import { join } from "node:path";
import tailwind from "bun-plugin-tailwind";

const root = import.meta.dir;
const outdir = join(root, "dist");
rmSync(outdir, { recursive: true, force: true });

const result = await Bun.build({
  entrypoints: [join(root, "index.html")],
  compile: true,
  target: "browser",
  outdir,
  minify: true,
  plugins: [tailwind],
});

if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
for (const output of result.outputs) {
  console.log(`${output.path}  ${(output.size / 1024).toFixed(1)} KiB`);
}
