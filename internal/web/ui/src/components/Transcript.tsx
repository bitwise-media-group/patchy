import { memo } from "preact/compat";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import type { Finding, TranscriptTurn, TurnKind } from "../types";
import { streamTranscript } from "../api";
import { formatCount } from "../format";
import { CodeBlock } from "../highlight";
import { Icon } from "./icons";
import { Markdown } from "./Markdown";

// Conversation renders one run's captured agent transcript, streaming it live
// while the run is in flight and replaying the stored record afterwards.
//
// It is a section inside the investigation and remediation tabs rather than a
// tab of its own: the conversation explains that run's report and verdict, and
// reading it beside them is the point.
export function Conversation({
  finding,
  kind,
}: {
  finding: Finding;
  kind: "investigation" | "remediation";
}) {
  const run = kind === "investigation" ? finding.investigation : finding.remediation;
  const live = finding.activeRun?.kind === kind;
  const attempt = run?.attempt ?? finding.attempts?.[kind];
  const recorded = run?.transcript?.turns ?? 0;

  // Nothing to show and nothing coming: the run never spoke, or its harness
  // cannot transcribe.
  if (!attempt || (recorded === 0 && !live)) return null;

  return (
    <section class="mt-6">
      <h2 class="ps-heading mb-3">Conversation</h2>
      <TranscriptStream
        finding={finding.name}
        kind={kind}
        attempt={attempt}
        live={live}
        truncated={run?.transcript?.truncated ?? false}
        archived={run?.transcript?.archived ?? false}
      />
    </section>
  );
}

function TranscriptStream({
  finding,
  kind,
  attempt,
  live,
  truncated,
  archived,
}: {
  finding: string;
  kind: "investigation" | "remediation";
  attempt: number;
  live: boolean;
  truncated: boolean;
  archived: boolean;
}) {
  const [turns, setTurns] = useState<TranscriptTurn[]>([]);
  const [state, setState] = useState<"loading" | "streaming" | "done" | "error">("loading");
  const [reconnecting, setReconnecting] = useState(false);
  const scroller = useRef<HTMLDivElement>(null);
  // Only follow the tail when the reader is already at it; yanking the view
  // away from someone reading back through the run is worse than not following.
  const pinned = useRef(true);

  useEffect(() => {
    setTurns([]);
    setState("loading");
    setReconnecting(false);
    pinned.current = true;

    // Turns are batched into one render per animation frame: joining a long
    // run replays thousands of turns in a burst, and a render per turn would
    // stall the page for seconds.
    let pending: TranscriptTurn[] = [];
    let frame = 0;
    const flush = () => {
      frame = 0;
      const batch = pending;
      pending = [];
      if (batch.length === 0) return;
      setTurns((prev) => prev.concat(batch));
      setState((prev) => (prev === "loading" ? "streaming" : prev));
    };
    const close = streamTranscript(finding, kind, attempt, {
      onTurn: (turn) => {
        pending.push(turn);
        if (!frame) frame = requestAnimationFrame(flush);
      },
      onEnd: () => {
        flush();
        setState("done");
      },
      onError: () => {
        flush();
        setState("error");
      },
      onReconnecting: setReconnecting,
    }, archived);
    return () => {
      close();
      if (frame) cancelAnimationFrame(frame);
    };
  }, [finding, kind, attempt, archived]);

  // Each tool_use by id, so a tool_result can borrow its call's path and be
  // highlighted as the file it read.
  const calls = useMemo(() => {
    const byID = new Map<string, TranscriptTurn>();
    for (const turn of turns) {
      if (turn.kind === "tool_use" && turn.id) byID.set(turn.id, turn);
    }
    return byID;
  }, [turns]);

  useEffect(() => {
    const el = scroller.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [turns]);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  };

  if (state === "loading") {
    return <p class="text-faint">Loading the conversation…</p>;
  }
  if (state === "error" && turns.length === 0) {
    return (
      <p class="text-faint">
        The conversation could not be loaded
        {archived ? " from the archive (the bucket must allow GET from this page's origin)" : ""}.
      </p>
    );
  }
  if (turns.length === 0) {
    return (
      <p class="text-faint">
        No conversation recorded{live ? " yet" : " (the run may have expired)"}.
      </p>
    );
  }

  return (
    <>
      <div
        ref={scroller}
        onScroll={onScroll}
        class="max-h-[32rem] overflow-y-auto ps-well"
      >
        <ol class="flex flex-col gap-3">
          {turns.map((turn) => (
            <TurnRow
              key={turn.seq}
              turn={turn}
              call={turn.kind === "tool_result" && turn.id ? calls.get(turn.id) : undefined}
            />
          ))}
        </ol>
      </div>
      <p class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px] text-muted">
        {state === "streaming" && reconnecting ? (
          <span class="inline-flex items-center gap-1.5 font-mono text-amber">reconnecting…</span>
        ) : state === "streaming" ? (
          <span class="inline-flex items-center gap-1.5 font-mono text-ink">
            <span class="ps-live-dot" /> streaming
          </span>
        ) : null}
        <span class="font-mono">{formatCount(turns.length)} turns</span>
        {archived ? <span class="font-mono">full record from the archive</span> : null}
        {truncated ? (
          <span class="inline-flex items-center gap-1">
            <Icon name="alertTriangle" size={13} />
            recording limits cut this conversation short
          </span>
        ) : null}
      </p>
    </>
  );
}

// TURN_LABEL names each kind in the reader's terms rather than the wire's.
const TURN_LABEL: Record<TurnKind, string> = {
  text: "assistant",
  thinking: "thinking",
  tool_use: "tool",
  tool_result: "result",
  notice: "session",
};

// TurnRow is memoised: turns are immutable once received, so each one is
// rendered (and highlighted) once, not on every batch that follows it.
const TurnRow = memo(function TurnRow({ turn, call }: { turn: TranscriptTurn; call?: TranscriptTurn }) {
  switch (turn.kind) {
    case "text":
      // Prose is the agent talking; render its markdown like the report does.
      return (
        <li class="flex flex-col gap-1">
          <TurnLabel turn={turn} />
          <div class="text-[13px] leading-relaxed">
            <Markdown source={turn.text ?? ""} />
          </div>
        </li>
      );
    case "thinking":
      return (
        <li class="flex flex-col gap-1">
          <TurnLabel turn={turn} />
          <p class="whitespace-pre-wrap break-words text-[12.5px] italic leading-relaxed text-faint">
            {turn.text}
            {turn.truncated ? <Truncated /> : null}
          </p>
        </li>
      );
    case "notice":
      return (
        <li class="flex items-center gap-2 text-[12px] text-faint">
          <span class="font-mono">{turn.text}</span>
        </li>
      );
    case "tool_use":
      return (
        <li class="flex flex-col gap-1">
          <TurnLabel turn={turn} />
          <CodeBlock
            code={turn.text ?? ""}
            {...toolUseLanguage(turn)}
            wrap={turn.syntax !== "diff"}
            trailer={turn.truncated ? <Truncated /> : null}
          />
        </li>
      );
    default: {
      const read = call && readsFile(call) && !(turn.text ?? "").startsWith("[error]");
      return (
        <li class="flex flex-col gap-1">
          <TurnLabel turn={turn} />
          <CodeBlock
            code={turn.text ?? ""}
            path={read ? call.path : undefined}
            gutter={!!read}
            wrap={!read}
            trailer={turn.truncated ? <Truncated /> : null}
          />
        </li>
      );
    }
  }
});

// readsFile reports whether a tool call only names a file (its text is
// exactly its path), so its result is that file's content. A call carrying
// a body (a write, an edit) or a render hint (a shell command) does not.
function readsFile(call: TranscriptTurn): boolean {
  return !!call.path && !call.syntax && call.text === call.path;
}

// toolUseLanguage picks how a tool call's own text is highlighted: its syntax
// hint, else the language of the file whose content it carries. A call whose
// text is just a path renders plain.
function toolUseLanguage(turn: TranscriptTurn): { lang?: string; path?: string } {
  if (turn.syntax === "diff") return { lang: "diff" };
  if (turn.syntax === "shell") return { lang: "bash" };
  if (turn.path && turn.text !== turn.path) return { path: turn.path };
  return {};
}

function Truncated() {
  return <span class="text-faint"> …truncated</span>;
}

function TurnLabel({ turn }: { turn: TranscriptTurn }) {
  return (
    <div class="flex items-center gap-2">
      <span class="ps-label">{TURN_LABEL[turn.kind] ?? turn.kind}</span>
      {turn.tool ? <span class="ps-mono-tag">{turn.tool}</span> : null}
      {turn.path && turn.kind === "tool_use" && turn.text !== turn.path ? (
        <span class="min-w-0 truncate font-mono text-[12px] text-muted">{turn.path}</span>
      ) : null}
      {turn.at ? <time class="text-[12px] text-faint">{clockTime(turn.at)}</time> : null}
    </div>
  );
}

// clockTime shows the time of day only: a transcript's turns are minutes
// apart, and the date is already on the run.
function clockTime(at: string): string {
  const parsed = new Date(at);
  if (Number.isNaN(parsed.getTime())) return "";
  return parsed.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}
