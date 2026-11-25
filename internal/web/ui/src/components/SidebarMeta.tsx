import type { ComponentChildren } from "preact";
import type { Finding } from "../types";
import { DASH, formatDate } from "../format";
import { Icon } from "./icons";
import { PullRequestCard } from "./PullRequest";

// Card is one section of the sidebar's single panel: the panel holds the
// border, each section only a divider, so the facts read as one record
// rather than a stack of boxes.
function Card({ title, children }: { title: string; children: ComponentChildren }) {
  return (
    <section class="border-t border-line px-4 py-3.5 first:border-t-0">
      <h3 class="mx-0 mt-0 mb-2 text-[13px] font-bold text-fg">{title}</h3>
      {children}
    </section>
  );
}

// RepoName sets "owner/name" as the owner on a quiet line above the name,
// so a long name never breaks mid-word in the narrow sidebar.
function RepoName({ name }: { name: string }) {
  const slash = name.lastIndexOf("/");
  if (slash < 0) return <span>{name}</span>;
  return (
    <span class="min-w-0">
      <span class="block text-[12px] font-normal text-muted">{name.slice(0, slash)}</span>
      <span class="block [overflow-wrap:anywhere]">{name.slice(slash + 1)}</span>
    </span>
  );
}

export function SidebarMeta({ finding }: { finding: Finding }) {
  const owners = new Set<string>(finding.owners ?? []);
  for (const e of finding.enrichments ?? []) for (const o of e.owners ?? []) owners.add(o);

  return (
    <aside
      class="mt-5 flex flex-col self-start overflow-hidden rounded-[11px] border border-line bg-surface max-lg:mt-1"
      aria-label="Finding metadata"
    >
      {finding.pullRequest ? (
        <Card title="Pull request">
          <PullRequestCard pr={finding.pullRequest} />
        </Card>
      ) : null}

      <Card title="Owners">
        {owners.size > 0 ? (
          <ul class="m-0 flex list-none flex-col gap-1.5 p-0 text-[12.5px]">
            {[...owners].map((o) => (
              <li key={o}>{o}</li>
            ))}
          </ul>
        ) : (
          <span class="text-faint">No owner resolved yet</span>
        )}
      </Card>

      <Card title="Repository">
        {finding.repository ? (
          <dl class="ps-kv ps-kv--stack">
            <div>
              <dt>Name</dt>
              <dd>
                <a
                  href={finding.repository.url}
                  target="_blank"
                  rel="noreferrer"
                  class="inline-flex items-start gap-1.5 font-semibold text-fg no-underline hover:underline"
                >
                  <Icon name="github" size={13} class="mt-[3px] flex-none" />
                  <RepoName name={finding.repository.name ?? finding.repository.url} />
                </a>
              </dd>
            </div>
            <div>
              <dt>Default branch</dt>
              <dd>{finding.repository.defaultBranch ?? DASH}</dd>
            </div>
          </dl>
        ) : (
          <span class="text-faint">None. This finding cannot be remediated automatically.</span>
        )}
      </Card>

      <Card title="Source">
        <dl class="ps-kv ps-kv--stack">
          <div>
            <dt>Scanner</dt>
            <dd>{finding.source ? <span class="ps-mono-tag">{finding.source}</span> : DASH}</dd>
          </div>
          <div>
            <dt>Rule</dt>
            <dd>{finding.ruleID ? <span class="ps-mono-tag">{finding.ruleID}</span> : DASH}</dd>
          </div>
          <div>
            <dt>Integration</dt>
            <dd>{finding.integration ?? DASH}</dd>
          </div>
          <div>
            <dt>Alerts</dt>
            <dd>
              {(finding.alerts?.length ?? 0) + (finding.overflowAlerts ?? 0)}
              {finding.overflowAlerts ? ` (${finding.overflowAlerts} overflow)` : ""}
            </dd>
          </div>
        </dl>
      </Card>

      <Card title="Tracking issue">
        {finding.tracking?.issueNumber ? (
          <a class="ps-mono-tag" href={finding.tracking.url} target="_blank" rel="noreferrer">
            #{finding.tracking.issueNumber} · {finding.tracking.state ?? "open"} <Icon name="externalLink" size={11} />
          </a>
        ) : (
          <span class="text-faint">Not yet projected</span>
        )}
      </Card>

      <Card title="Advisories">
        <ul class="m-0 flex list-none flex-col gap-1.5 p-0">
          {finding.advisories.map((a, i) => (
            <li key={a}>
              <span class="ps-mono-tag">{a}</span>
              {i === 0 ? <span class="ml-1 text-[12px] text-faint">authoritative</span> : null}
            </li>
          ))}
        </ul>
      </Card>

      <Card title="Dates">
        <dl class="ps-kv ps-kv--stack">
          <div>
            <dt>First observed</dt>
            <dd>{formatDate(finding.firstObservedAt)}</dd>
          </div>
          <div>
            <dt>Created</dt>
            <dd>{formatDate(finding.createdAt)}</dd>
          </div>
          <div>
            <dt>Completed</dt>
            <dd>{formatDate(finding.completedAt)}</dd>
          </div>
        </dl>
      </Card>
    </aside>
  );
}
