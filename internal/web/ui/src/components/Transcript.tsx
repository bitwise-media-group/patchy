import { useEffect, useRef, useState } from "preact/hooks";
import type { Finding, TranscriptTurn, TurnKind } from "../types";
import { streamTranscript } from "../api";
import { formatCount } from "../format";
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
}: {
  finding: string;
  kind: "investigation" | "remediation";
  attempt: number;
  live: boolean;
  truncated: boolean;
}) {
  const [turns, setTurns] = useState<TranscriptTurn[]>([]);
  const [state, setState] = useState<"loading" | "streaming" | "done" | "error">("loading");
  const scroller = useRef<HTMLDivElement>(null);
  // Only follow the tail when the reader is already at it; yanking the view
  // away from someone reading back through the run is worse than not following.
  const pinned = useRef(true);

  useEffect(() => {
    setTurns([]);
    setState("loading");
    pinned.current = true;
    const close = streamTranscript(finding, kind, attempt, {
      onTurn: (turn) => {
        setTurns((prev) => [...prev, turn]);
        setState((prev) => (prev === "loading" ? "streaming" : prev));
      },
      onEnd: () => setState("done"),
      onError: () => setState("error"),
    });
    return close;
  }, [finding, kind, attempt]);

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
    return <p class="text-faint">The conversation could not be loaded.</p>;
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
        class="max-h-[32rem] overflow-y-auto rounded-[11px] border border-line bg-code p-4"
      >
        <ol class="flex flex-col gap-3">
          {turns.map((turn) => (
            <TurnRow key={turn.seq} turn={turn} />
          ))}
        </ol>
      </div>
      <p class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[10.5px] text-muted">
        {state === "streaming" ? (
          <span class="inline-flex items-center gap-1.5 font-mono text-ink">
            <span class="ps-live-dot" /> streaming
          </span>
        ) : null}
        <span class="font-mono">{formatCount(turns.length)} turns</span>
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

function TurnRow({ turn }: { turn: TranscriptTurn }) {
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
    case "notice":
      return (
        <li class="flex items-center gap-2 text-[11px] text-faint">
          <span class="font-mono">{turn.text}</span>
        </li>
      );
    default:
      return (
        <li class="flex flex-col gap-1">
          <TurnLabel turn={turn} />
          <pre class="overflow-x-auto whitespace-pre-wrap break-words rounded-md border border-line bg-code-2 px-2.5 py-2 font-mono text-[11.5px] leading-relaxed text-fg">
            {turn.text}
            {turn.truncated ? <span class="text-faint"> …truncated</span> : null}
          </pre>
        </li>
      );
  }
}

function TurnLabel({ turn }: { turn: TranscriptTurn }) {
  return (
    <div class="flex items-center gap-2">
      <span class="ps-label">{TURN_LABEL[turn.kind] ?? turn.kind}</span>
      {turn.tool ? <span class="ps-mono-tag">{turn.tool}</span> : null}
      {turn.at ? <time class="text-[10px] text-faint">{clockTime(turn.at)}</time> : null}
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
