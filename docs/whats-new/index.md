---
version: 0.15.0 # x-release-please-version
title: "What's new in Patchy 0.15.0" # x-release-please-version
# Shot into docs/assets/images/whats-new/<version>/ by the release PR's
# workflow (release-screenshots.yaml, running internal/web/ui/screenshot.ts):
# name, the demo route to load, and optionally text to type into the
# findings filter. Preview locally with `mise run ui-screenshots`.
screenshots:
  # Repository filters: "starting with" and "containing" the typed term.
  - name: filter-typeahead
    hash: "#/"
    type: mobile
  # A shared link: severities plus additive repository terms, from the URL.
  - name: filter-link
    hash: "#/?severity=critical&severity=high&repoPrefix=mobile&repoContains=ledger"
  # My repositories: the tracked-repository list with counts by phase.
  - name: repositories
    hash: "#/repositories"
---

<!--
  The current release's page. Its version and title (the page heading) live in the front matter, and release-please
  rewrites both marked lines when it opens a release PR (release-please-config.json, extra-files). Keep every other
  version string, image paths included, off those lines. After each release, the What's New workflow freezes this
  page into <version>.md and starts it again (hack/whats-new-freeze.sh; AGENTS.md, "What's new").
-->

This release brings **GitHub secret scanning** into the pipeline, rebuilds the **status page's findings list** for
backlogs that run to millions, and gives maintainers **My repositories**, a view of just the repositories they look
after. The full list of changes is in the [release notes](https://github.com/bitwise-media-group/patchy/releases) and
the [changelog](https://github.com/bitwise-media-group/patchy/blob/main/CHANGELOG.md).

## Leaked secrets, triaged without ever seeing them

Patchy now ingests GitHub **secret scanning** alerts as findings, alongside code scanning. Turn it on with one
capability on the GitHub `Integration`:

```yaml
spec:
  github:
    secretScanningAlerts:
      enabled: true
```

- **The secret never enters Patchy.** The webhook delivery carries the plaintext, so the receiver reads only the action,
  alert number and repository from it and fetches the alert again with the value hidden. The secret is not in the
  `Finding`, the tracking issue, the prompts or the logs. In the agent's pod, where the repository holds the value, it
  is cut out of the transcript and the report before anything is stored.
- **Severity follows risk.** An active or publicly leaked credential is `critical`, an inactive one `medium`.
- **Rotation comes first.** A leaked credential is not fixed by a code change, so a secret finding never becomes an
  unattended pull request. The agent decides whether the value is live, a placeholder or a test fixture. Confident false
  positives are resolved on GitHub with the right reason (`false_positive`, `used_in_tests` or `wont_fix`). Real leaks
  wait for a person, and the cleanup pull request opens with "Rotate this credential before merging".
- **Backfill covers it too.** One backfill now lists open code scanning and secret scanning alerts together.

Read more in [GitHub code and secret scanning](../integrations/sources/github.md#secret-scanning).

## A findings list built for a million findings

The status page used to download every finding on each load and on every change, then filter and sort them in the
browser. With an onboarding backlog in the hundreds of thousands, that does not work. Now the server does the work, and
the page loads 50 rows at a time as you scroll.

- **Filtering and counts happen on the server.** The phase rail, the summary and the filter's counts always cover every
  finding, not just the rows you have loaded.
- **Rows stay put while you read.** When findings change while you are scrolled down the list, the rows you are reading
  stay where they are, and an **Updated. Show the latest** button appears.
- **Pages never repeat or skip a row**, even while findings change under you.

Measured on a laptop, in process, one request each:

| At 100,000 findings                                 | Time   |
| --------------------------------------------------- | ------ |
| First page of the list, with every count            | 5.9 ms |
| A text search across titles, rules and repositories | 11 ms  |
| The filter's suggestions for one keystroke          | 8 ms   |

The cost per finding is the same at 10,000 and 100,000 findings (56 and 59 ns), so it grows in a straight line. At
10,000 findings, a first page through the real server takes 4 ms and is 2.7 KiB on the wire.

Each request now also reads the findings without copying them. The server used to copy every finding out of its cache on
each request. At 100,000 findings that copy alone was 75 MB of short-lived memory and at least 6 ms; now it is under 1
MB and 0.02 ms. At a 2-million-finding backlog, that is about 1.5 GB less memory churned per request.

## Filter repositories by name

Type part of a repository name in the findings filter and pick **Repositories starting with** or **Repositories
containing** it, to cover every matching repository at once. Each option shows how many repositories it covers.
Repository filters now add up the way severities do: pick several repositories, or several name patterns, and the list
shows findings from any of them.

![Typing "mobile" offers repositories starting with and containing it](../assets/images/whats-new/0.15.0/filter-typeahead.jpg#only-dark)
![Typing "mobile" offers repositories starting with and containing it](../assets/images/whats-new/0.15.0/filter-typeahead-light.jpg#only-light)

## Share a filtered view

The findings filter is now part of the page address, so you can bookmark a view or send it to someone and it opens with
the same filter. For example, critical and high findings in repositories whose names start with `payments`:

```text
https://patchy.example.com/#/?severity=critical&severity=high&repoPrefix=payments
```

Going back to the list from a finding keeps your filter too.

![A shared link opens with its filter applied](../assets/images/whats-new/0.15.0/filter-link.jpg#only-dark)
![A shared link opens with its filter applied](../assets/images/whats-new/0.15.0/filter-link-light.jpg#only-light)

## My repositories

Most people do not look after the whole estate. They look after a few repositories. The new **My repositories** view
puts those in one place, so a maintainer can see at a glance what patchy is doing for them.

- **Your list, in your browser.** Add repositories by name, with suggestions as you type. The list stays in your
  browser, so there is nothing to set up and nobody else's view changes. Export it as JSON to move it or to share a
  starting list with your team, and import it anywhere.
- **Every finding, by where it stands.** Each row counts the repository's findings as open, awaiting approval, in
  progress, in review, remediated, failed or closed. Every count opens the findings list filtered to that repository and
  those phases, and the row links to the repository's all-time metrics.
- **Find out why a repository is quiet.** For a repository patchy has never seen, the page asks whether patchy can read
  its alerts at all. If it cannot, it says why: the GitHub App is not installed, the credential lacks
  `security_events: read`, or Advanced Security is off. It also warns when no Forge could push a fix there.
- **Backfill one repository.** When patchy can read the alerts but has none yet, people with the `backfill` grant can
  pull in that one repository's open alerts with two clicks. Everyone else is told to ask an operator. The button only
  appears for repositories patchy has never seen, and the server refuses the rest, so the page cannot be used to re-walk
  repositories that webhooks already keep up to date.

The access check runs in the integration-controller, which already holds the Integrations' GitHub credentials, on a new
in-cluster port that only the status server can reach. Its answers are cached for five minutes. Read more in
[The status page](../operate/status-page.md#my-repositories).

![My repositories, with finding counts by phase](../assets/images/whats-new/0.15.0/repositories.jpg#only-dark)
![My repositories, with finding counts by phase](../assets/images/whats-new/0.15.0/repositories-light.jpg#only-light)

## A bigger demo

The [demo](../demo.md) now holds 611 findings across 211 repositories, so infinite scroll, the counts and the repository
filters can be tried without a cluster.

## Upgrading

- **`GET /api/findings` returns one page.** If you call the status server's API directly, the response now holds one
  page of findings with `matched` and `nextCursor` instead of every finding. Follow `nextCursor` for the rest. The
  parameters are in the [status-server reference](../configuration/status-server.md).
- **`GET /api/rollups` no longer has a `findings` field.** It was always empty.
- **The integration-controller listens on a new in-cluster port, 9792.** The kustomize base and the Helm chart wire it
  up for you: the Service port, a TokenReview grant for the integration-controller, a projected token in the status
  server, and the NetworkPolicy rules between the two. If you deploy from your own manifests, add the same pieces; see
  [Deploying](https://github.com/bitwise-media-group/patchy/blob/main/deploy/README.md). Without them the access check
  fails, and the rest of the repositories view still works.
