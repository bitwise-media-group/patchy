// Minimal markdown renderer for scanner-supplied text (finding descriptions,
// enrichment notes). Builds vnodes — never innerHTML — so untrusted input
// cannot inject markup. Covers the GHAS/CodeQL help subset: headings, fenced
// code, lists, blockquotes, inline code, bold/italic, and http(s) links.

import type { ComponentChildren } from "preact";

const INLINE_RE = /`([^`\n]+)`|\*\*([^*\n]+)\*\*|\*([^*\s][^*\n]*)\*|\[([^\]\n]+)\]\(([^)\s]+)\)/g;

function renderInline(text: string): ComponentChildren[] {
  const out: ComponentChildren[] = [];
  let last = 0;
  for (const m of text.matchAll(INLINE_RE)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    if (m[1] !== undefined) {
      out.push(
        <code class="rounded-[5px] border border-line bg-code-2 px-1 py-px font-mono text-[0.92em] text-fg">
          {m[1]}
        </code>,
      );
    } else if (m[2] !== undefined) {
      out.push(<strong class="font-semibold text-fg">{renderInline(m[2])}</strong>);
    } else if (m[3] !== undefined) {
      out.push(<em>{renderInline(m[3])}</em>);
    } else if (/^https?:\/\//.test(m[5])) {
      out.push(
        <a class="font-semibold text-ink" href={m[5]} target="_blank" rel="noreferrer">
          {renderInline(m[4])}
        </a>,
      );
    } else {
      // Non-http(s) targets (javascript:, relative paths with nothing to be
      // relative to) render as plain text.
      out.push(m[4]);
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

type Block =
  | { kind: "heading"; depth: number; text: string }
  | { kind: "code"; text: string }
  | { kind: "list"; ordered: boolean; items: string[] }
  | { kind: "quote"; text: string }
  | { kind: "para"; text: string };

const ITEM_RE = /^\s*(?:[-*]|\d+\.)\s+/;

function parseBlocks(source: string): Block[] {
  const lines = source.replace(/\r\n/g, "\n").split("\n");
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }
    if (/^```/.test(line)) {
      const body: string[] = [];
      i++;
      while (i < lines.length && !/^```\s*$/.test(lines[i])) body.push(lines[i++]);
      i++;
      blocks.push({ kind: "code", text: body.join("\n") });
      continue;
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      blocks.push({ kind: "heading", depth: heading[1].length, text: heading[2] });
      i++;
      continue;
    }
    if (ITEM_RE.test(line)) {
      const ordered = /^\s*\d+\./.test(line);
      const items: string[] = [];
      while (i < lines.length && ITEM_RE.test(lines[i])) items.push(lines[i++].replace(ITEM_RE, ""));
      blocks.push({ kind: "list", ordered, items });
      continue;
    }
    if (/^>\s?/.test(line)) {
      const body: string[] = [];
      while (i < lines.length && /^>\s?/.test(lines[i])) body.push(lines[i++].replace(/^>\s?/, ""));
      blocks.push({ kind: "quote", text: body.join(" ") });
      continue;
    }
    const body: string[] = [];
    while (i < lines.length && lines[i].trim() && !/^(#{1,6}\s|```|>\s?)/.test(lines[i]) && !ITEM_RE.test(lines[i])) {
      body.push(lines[i++]);
    }
    blocks.push({ kind: "para", text: body.join(" ") });
  }
  return blocks;
}

function renderBlock(block: Block, key: number): ComponentChildren {
  switch (block.kind) {
    case "heading": {
      const cls =
        block.depth <= 2
          ? "mt-5 mb-1.5 text-[15px] font-semibold tracking-tight text-fg first:mt-0"
          : "mt-4 mb-1 text-[13.5px] font-semibold text-fg first:mt-0";
      return (
        <h3 key={key} class={cls}>
          {renderInline(block.text)}
        </h3>
      );
    }
    case "code":
      return (
        <pre
          key={key}
          class="my-3 overflow-x-auto rounded-[9px] border border-line bg-code-2 px-3 py-2.5 font-mono text-[11.5px] leading-relaxed text-fg"
        >
          <code>{block.text}</code>
        </pre>
      );
    case "list": {
      const items = block.items.map((item, j) => <li key={j}>{renderInline(item)}</li>);
      const cls = "my-2 flex flex-col gap-1 pl-5";
      return block.ordered ? (
        <ol key={key} class={`${cls} list-decimal`}>
          {items}
        </ol>
      ) : (
        <ul key={key} class={`${cls} list-disc`}>
          {items}
        </ul>
      );
    }
    case "quote":
      return (
        <blockquote key={key} class="my-2 border-l-2 border-line-2 pl-3 text-faint">
          {renderInline(block.text)}
        </blockquote>
      );
    case "para":
      return (
        <p key={key} class="my-2 first:mt-0 last:mb-0">
          {renderInline(block.text)}
        </p>
      );
  }
}

export function Markdown({ source }: { source: string }) {
  return (
    <div class="text-[13.5px] leading-relaxed text-muted">
      {parseBlocks(source).map((b, i) => renderBlock(b, i))}
    </div>
  );
}
