// Syntax highlighting for every place the page shows code: fenced blocks in
// markdown, and tool calls and results in an agent conversation.
//
// lowlight runs the highlight.js grammars and returns a hast tree, which is
// turned into vnodes here — never innerHTML, the same rule Markdown.tsx keeps
// for untrusted text. Only a curated set of grammars is registered (not
// lowlight's `common`): the page is a single-file bundle embedded in the Go
// binary, so every grammar is paid for on every page load.

import { createLowlight } from "lowlight";
import type { ComponentChildren } from "preact";
import { memo } from "preact/compat";
import { useMemo } from "preact/hooks";
import bash from "highlight.js/lib/languages/bash";
import c from "highlight.js/lib/languages/c";
import cpp from "highlight.js/lib/languages/cpp";
import csharp from "highlight.js/lib/languages/csharp";
import css from "highlight.js/lib/languages/css";
import diff from "highlight.js/lib/languages/diff";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import go from "highlight.js/lib/languages/go";
import ini from "highlight.js/lib/languages/ini";
import java from "highlight.js/lib/languages/java";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import kotlin from "highlight.js/lib/languages/kotlin";
import makefile from "highlight.js/lib/languages/makefile";
import markdown from "highlight.js/lib/languages/markdown";
import php from "highlight.js/lib/languages/php";
import python from "highlight.js/lib/languages/python";
import ruby from "highlight.js/lib/languages/ruby";
import rust from "highlight.js/lib/languages/rust";
import shell from "highlight.js/lib/languages/shell";
import sql from "highlight.js/lib/languages/sql";
import typescript from "highlight.js/lib/languages/typescript";
import xml from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";

const lowlight = createLowlight({
  bash,
  c,
  cpp,
  csharp,
  css,
  diff,
  dockerfile,
  go,
  ini,
  java,
  javascript,
  json,
  kotlin,
  makefile,
  markdown,
  php,
  python,
  ruby,
  rust,
  shell,
  sql,
  typescript,
  xml,
  yaml,
});

// The subset auto-detection may pick from. Shell-session and markdown are
// left out: both match nearly anything and would win on prose.
const AUTO_SUBSET = lowlight
  .listLanguages()
  .filter((name) => name !== "shell" && name !== "markdown");

// Highlighting is skipped above these sizes: the grammars are regex-driven,
// and a multi-thousand-line tool result is read by scrolling, not by colour.
const MAX_BYTES = 64 * 1024;
const MAX_LINES = 2000;

// Auto-detected results below this relevance are rendered plain; a low score
// means the grammar matched by accident.
const MIN_AUTO_RELEVANCE = 5;

type HastNode = ReturnType<typeof lowlight.highlight>["children"][number];

function toVNodes(nodes: HastNode[]): ComponentChildren[] {
  return nodes.map((node, i) => {
    if (node.type === "text") return node.value;
    if (node.type !== "element") return null;
    const cls = node.properties?.className;
    const className = Array.isArray(cls) ? cls.join(" ") : typeof cls === "string" ? cls : undefined;
    return (
      <span key={i} class={className}>
        {toVNodes(node.children as HastNode[])}
      </span>
    );
  });
}

function tooBig(code: string): boolean {
  if (code.length > MAX_BYTES) return true;
  let lines = 1;
  for (let i = 0; i < code.length; i++) {
    if (code.charCodeAt(i) === 10 && ++lines > MAX_LINES) return true;
  }
  return false;
}

// resolveLang returns the registered grammar name for a fence tag or language
// hint, or undefined when there is none.
export function resolveLang(lang?: string): string | undefined {
  const name = lang?.trim().toLowerCase();
  if (!name) return undefined;
  return lowlight.registered(name) ? name : undefined;
}

// highlight renders code as highlighted vnodes. With no language it renders
// plain unless auto is set, in which case it guesses from the registered
// subset and keeps the guess only when it is confident. Oversized input is
// always plain.
export function highlight(code: string, lang?: string, auto = false): ComponentChildren {
  const name = resolveLang(lang);
  if ((!name && !auto) || tooBig(code)) return code;
  try {
    const tree = name ? lowlight.highlight(name, code) : lowlight.highlightAuto(code, { subset: AUTO_SUBSET });
    if (!name && (tree.data?.relevance ?? 0) < MIN_AUTO_RELEVANCE) return code;
    return toVNodes(tree.children);
  } catch {
    // A grammar failing on odd input costs the colour, not the text.
    return code;
  }
}

// Extension → grammar. Keys are lower-case, without the dot.
const EXT_LANG: Record<string, string> = {
  go: "go",
  ts: "typescript",
  tsx: "typescript",
  mts: "typescript",
  cts: "typescript",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  py: "python",
  pyi: "python",
  java: "java",
  kt: "kotlin",
  kts: "kotlin",
  rs: "rust",
  c: "c",
  h: "c",
  cc: "cpp",
  cpp: "cpp",
  cxx: "cpp",
  hh: "cpp",
  hpp: "cpp",
  hxx: "cpp",
  cs: "csharp",
  rb: "ruby",
  php: "php",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  yaml: "yaml",
  yml: "yaml",
  json: "json",
  jsonc: "json",
  toml: "ini",
  ini: "ini",
  cfg: "ini",
  conf: "ini",
  properties: "ini",
  xml: "xml",
  html: "xml",
  htm: "xml",
  svg: "xml",
  xsd: "xml",
  plist: "xml",
  css: "css",
  scss: "css",
  sql: "sql",
  diff: "diff",
  patch: "diff",
  md: "markdown",
  markdown: "markdown",
  mk: "makefile",
  dockerfile: "dockerfile",
};

// Whole basenames that carry no telling extension.
const NAME_LANG: Record<string, string> = {
  dockerfile: "dockerfile",
  containerfile: "dockerfile",
  makefile: "makefile",
  gnumakefile: "makefile",
  "go.mod": "go",
  ".bashrc": "bash",
  ".zshrc": "bash",
};

// langFromPath infers a grammar from a file path, or undefined.
// Terraform (.tf/.hcl) has no highlight.js grammar and stays plain.
export function langFromPath(path?: string): string | undefined {
  if (!path) return undefined;
  const base = path.split(/[/\\]/).pop()?.toLowerCase() ?? "";
  if (NAME_LANG[base]) return NAME_LANG[base];
  if (base.startsWith("dockerfile.") || base.startsWith("containerfile.")) return "dockerfile";
  const dot = base.lastIndexOf(".");
  if (dot < 0) return undefined;
  return EXT_LANG[base.slice(dot + 1)];
}

// A numbered line from a file-reading tool: `cat -n` style ("    12\tcode")
// or claude's arrow style ("    12→code").
const GUTTER_RE = /^\s*(\d+)(?:\t|→)(.*)$/;

interface Gutter {
  start: number;
  lines: string[];
  // rest is whatever followed the numbered run (a tool's trailing note),
  // shown plain beneath the code.
  rest: string;
}

// splitGutter lifts a leading run of consecutively numbered lines out of
// text, or returns null when text is not numbered output.
export function splitGutter(text: string): Gutter | null {
  const lines = text.split("\n");
  const first = GUTTER_RE.exec(lines[0] ?? "");
  if (!first) return null;
  const start = Number(first[1]);
  const body: string[] = [];
  let i = 0;
  for (; i < lines.length; i++) {
    const m = GUTTER_RE.exec(lines[i]);
    if (!m || Number(m[1]) !== start + i) break;
    body.push(m[2]);
  }
  return { start, lines: body, rest: lines.slice(i).join("\n").trim() };
}

const PRE_CLASS =
  "hljs overflow-x-auto rounded-md border border-line bg-code-2 px-2.5 py-2 font-mono text-[11.5px] leading-relaxed text-fg";

// CodeBlock renders code highlighted by lang, else by the grammar path implies.
// gutter lifts numbered file-read output into a line-number column. wrap keeps
// long lines wrapping (tool output); without it they scroll (source code).
export const CodeBlock = memo(function CodeBlock({
  code,
  lang,
  path,
  gutter = false,
  auto = false,
  wrap = false,
  trailer,
  class: extra,
}: {
  code: string;
  lang?: string;
  path?: string;
  gutter?: boolean;
  auto?: boolean;
  wrap?: boolean;
  trailer?: ComponentChildren;
  class?: string;
}) {
  const language = resolveLang(lang) ?? langFromPath(path);
  const split = useMemo(() => (gutter ? splitGutter(code) : null), [code, gutter]);
  const body = split ? split.lines.join("\n") : code;
  const rendered = useMemo(() => highlight(body, language, auto), [body, language, auto]);
  const cls = `${PRE_CLASS} ${extra ?? ""}`;

  if (split && split.lines.length > 0) {
    const numbers = split.lines.map((_, i) => split.start + i).join("\n");
    return (
      <div class={cls}>
        <div class="flex gap-3">
          <pre aria-hidden="true" class="select-none text-right text-faint">
            {numbers}
          </pre>
          <pre class="min-w-0 flex-1 overflow-x-auto">
            <code>{rendered}</code>
          </pre>
        </div>
        {split.rest ? <pre class="mt-2 whitespace-pre-wrap break-words text-faint">{split.rest}</pre> : null}
        {trailer}
      </div>
    );
  }
  return (
    <pre class={`${cls} ${wrap ? "whitespace-pre-wrap break-words" : ""}`}>
      <code>{rendered}</code>
      {trailer}
    </pre>
  );
});
