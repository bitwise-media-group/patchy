import { useState } from "preact/hooks";
import type { ArchiveObject } from "../types";
import { fetchArchiveURL } from "../api";
import { Icon } from "./icons";

// ArchiveLink downloads one archived object of a run. The URL is presigned on
// click, never rendered into the page: it is a short-lived bearer capability,
// so it is fetched right before use and handed straight to the browser. The
// object is served as an attachment, so following it downloads rather than
// navigates.
export function ArchiveLink({
  finding,
  kind,
  attempt,
  object,
  label,
}: {
  finding: string;
  kind: "investigation" | "remediation";
  attempt: number;
  object: ArchiveObject;
  label: string;
}) {
  const [state, setState] = useState<"idle" | "busy" | "error">("idle");
  const onClick = async () => {
    setState("busy");
    try {
      const { url } = await fetchArchiveURL(finding, kind, attempt, object);
      const a = document.createElement("a");
      a.href = url;
      a.download = `${finding}-${kind}-${attempt}-${object}`;
      a.rel = "noreferrer";
      document.body.appendChild(a);
      a.click();
      a.remove();
      setState("idle");
    } catch {
      setState("error");
    }
  };
  return (
    <button type="button" class="ps-action" disabled={state === "busy"} onClick={onClick}>
      <Icon name="download" size={13} />
      {state === "busy" ? "Preparing…" : state === "error" ? `${label} unavailable` : label}
    </button>
  );
}
