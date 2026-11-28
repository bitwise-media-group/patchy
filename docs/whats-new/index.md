---
version: 0.16.1 # x-release-please-version
title: "What's new in Patchy 0.16.1" # x-release-please-version
# Shot into docs/assets/images/whats-new/<version>/ by the release PR's
# workflow (release-screenshots.yaml, running internal/web/ui/screenshot.ts):
# name, the demo route to load, and optionally text to type into the
# findings filter. Preview locally with `mise run ui-screenshots`.
screenshots:
  # My repositories with a connected GitHub account: imported repositories,
  # the backfill paths and the operators' Backfill requests panel.
  - name: repositories-github
    hash: "#/repositories"
---

<!--
  The current release's page. Its version and title (the page heading) live in the front matter, and release-please
  rewrites both marked lines when it opens a release PR (release-please-config.json, extra-files). Keep every other
  version string, image paths included, off those lines. After each release, the What's New workflow freezes this
  page into <version>.md and starts it again (hack/whats-new-freeze.sh; AGENTS.md, "What's new").
-->

This release lets maintainers **connect their GitHub account** to the status page, so **My repositories** fills itself
with the repositories they look after and they can backfill, approve and expedite there without a namespace-wide grant.
Backfill becomes **its own kind of request**: anyone can ask for one, operators approve or deny it, and a repository can
be **backfilled again** once a day. The full list of changes is in the
[release notes](https://github.com/bitwise-media-group/patchy/releases) and the
[changelog](https://github.com/bitwise-media-group/patchy/blob/main/CHANGELOG.md).

## Connect GitHub, and import the repositories you maintain

Until now, My repositories was a list you typed in by hand. Now, where an operator has turned it on, the page offers
**Connect GitHub**. One trip to GitHub links your GitHub account to the person you are signed in as, and the page can
then list every repository you are admin or maintain on. Pick the ones you want and they join your list.

- **Sign-in does not change.** You still sign in through your SSO provider, such as dex, whose groups drive patchy's
  RBAC. GitHub is a linked account beside it, not a second way in.
- **It reaches only what it should.** The link uses the GitHub App patchy already runs on, so it sees only repositories
  where that App is installed, with no more than the App's own permissions. A GitHub OAuth App would have needed the
  `repo` scope: read and write access to all of your code.
- **The status page never calls GitHub.** The integration-controller, which already holds the App's credentials, does
  the code exchange, refreshes the 8-hour tokens and asks GitHub about roles. The status page keeps your tokens in a
  sealed cookie and nowhere else.

Operators turn it on per github Integration with the App's client ID, its client secret in the Integration's Secret, and
a callback URL on the App. See [Connect GitHub](../integrations/sources/github.md#connect-github).

![My repositories, connected to GitHub](../assets/images/whats-new/0.16.0/repositories-github.jpg#only-dark)
![My repositories, connected to GitHub](../assets/images/whats-new/0.16.0/repositories-github-light.jpg#only-light)

## Maintainers act on their own repositories

Three new RBAC verbs give maintainers the buttons they need, on their repositories only:

- `backfill-maintained` runs a one-repository backfill at once;
- `approve-maintained` approves a held finding or revives a handed-off one;
- `expedite-maintained` moves a finding to the front of the queues.

Each works only where your linked GitHub account is admin or maintain, and patchy asks GitHub for your role again every
time you press the button, so losing maintain on GitHub takes the power away at once. That makes them safe to grant to a
broad group, such as every engineer who signs in through dex. The example RBAC ships a ready-made `patchy-maintainer`
role for exactly that. These verbs work on the status page only: the CLI and direct API writes still need the
namespace-wide verbs. See the [access model](../operate/status-page.md#access-model).

## Ask for a backfill, and let an operator decide

Anyone who can see findings can now ask for a repository to be backfilled. Without a grant, the button reads **Request
backfill**, and the row follows the request until an operator decides. Operators see a **Backfill requests** panel on
the same page, with **Approve** and **Deny** and an optional reason the requester sees. From a terminal, the same queue
is `patchy get backfillrequests`, `patchy approve backfillrequest <name>` and
`patchy deny backfillrequest <name> --reason "..."`, and `patchy backfill <integration> --request` files a request.

Behind it, each backfill is now its own `BackfillRequest` object rather than a field on the Integration. Several can
wait at once, each keeps its own outcome (alerts listed and ingested, whether the walk was truncated, the last error),
and the integration-controller walks them one at a time. A failing walk retries with backoff, and finished requests are
deleted after a week. See [The manual backfill](../integrations/sources/github.md#the-manual-backfill).

## Backfill again, once a day

A repository patchy already knows can now be backfilled too, to pick up alerts its webhooks missed. After a backfill of
a repository completes, the row offers **Backfill again** once the cooldown has passed, 24 hours by default, and says
when that will be. The same limits hold on every path, so the page cannot become a way to hammer the GitHub API: one
open request per repository, one walk per repository per cooldown, and at most 10 requests per person waiting for an
operator. Operators set the cooldown with the status server's `--repository-backfill-cooldown`.

## Upgrading

This release has breaking changes.

- **`Integration.spec.backfill` and `status.backfill` are gone.** A backfill is now a `BackfillRequest`. Remove
  `spec.backfill` from any Integration manifests you keep in git, and create a request instead:
  `patchy backfill <integration>` does it for you, or apply one directly (the shape is in
  [The manual backfill](../integrations/sources/github.md#the-manual-backfill)). A walk's outcome now lives on the
  request's status, so tooling that read `status.backfill` should read `kubectl get backfillrequests` instead.
- **Apply the new CRD and admission policy.** `BackfillRequest` has its own CRD, and a new ValidatingAdmissionPolicy,
  `patchy-backfill-requests`, decides who may create and decide one. The kustomize base and the Helm chart include both.
  The chart installs it while `crds.install` is on (the default); if you manage CRDs yourself, apply
  `deploy/kustomize/base/crds/patchy.bitwisemedia.uk_backfillrequests.yaml` before upgrading the controllers.
- **Both ServiceAccounts need new RBAC.** The integration-controller needs `get`, `list`, `watch` and `delete` on
  `backfillrequests` and `update` and `patch` on `backfillrequests/status`. The status server needs `get`, `list`,
  `watch`, `create` and `update` on `backfillrequests`. The kustomize base and the Helm chart grant these; add them if
  you deploy from your own manifests.
- **People who backfill from the CLI need native grants on `backfillrequests`.** The `backfill` verb on integrations
  still decides who may run one, but the CLI now creates a `BackfillRequest` instead of writing the Integration. Give
  those users `create`, `get`, `list` and `watch` on `backfillrequests`, plus `update` to approve or deny requests.
  Users who should only ask need `create`, and the admission policy keeps their requests waiting for an operator.
  `deploy/kustomize/base/rbac.users.example.yaml` shows every role.
- **The status server makes 14 access reviews per signed-in user**, up from 11, for the three maintainer verbs. They are
  cached for 20 seconds as before.
