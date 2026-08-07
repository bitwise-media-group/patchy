# Resolving a repository

Everything in the pipeline past triage needs a SHA-pinned repository to work in, and a cloud finding names none — it is
about a **resource**, not a file. The context enhancers close that gap: each looks the finding's resource up in its
cloud's inventory, carries the resource's metadata onto the finding as attributes, and resolves the owning repository
from **ownership labels or tags on the resource itself**.

One rule is shared by every enhancer in this section: it keys purely on the finding's cloud resource, never on which
source ingested it. A Google Cloud resource gets the [Cloud Asset Inventory lookup](google-cai.md) whether
[SCC](../sources/google-scc.md) or [Wiz](../sources/wiz.md) raised the finding, and an AWS or Azure resource gets its
[tags](aws.md) [lookup](azure.md) the same way — a [generic source's](../sources/generic.md) cloud findings included.

The chain runs in the context-controller in a fixed order — the cloud lookups, then the
[generic HTTP fan-out](generic.md), then the static context file — and each entry stands aside unless an Integration
enables it. [context-controller](../../configuration/context-controller.md) describes the chain's runtime behavior.

## The ownership labels

--8<-- ".snippets/ownership-labels.md"

The URL form supersedes the triple, and is the only one that can name a self-hosted forge. The vocabulary is the same on
every cloud; only the spelling constraints differ:

| Cloud        | Spelled as      | Read from                       | Can values carry `://`?                                                                                             |
| ------------ | --------------- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| Google Cloud | resource labels | Cloud Asset Inventory           | No — label values are lowercase, 63 characters; the scheme is added when absent. Security marks have no such limit. |
| AWS          | resource tags   | AWS Config or Resource Explorer | Yes, verbatim — though the scheme is still optional, for a vocabulary shared across clouds.                         |
| Azure        | resource tags   | Azure Resource Graph            | Yes, verbatim — the scheme is still optional.                                                                       |

The key names are configurable per provider — `cloudAssetInventory.labels` on the `google-cloud` Integration,
`resourceTags.tags` on `aws` and `azure` — for estates with an existing convention.

## When no repository resolves

Most resources carry no ownership labels or tags, and that is not a failure — nor is a resource the inventory simply has
no record of. Such a finding still ingests, still keeps its enrichment attributes, and still reaches a human; it just
cannot be remediated automatically, so the investigation gate hands it off. Set
[`github.issues.fallbackRepository`](../sources/github.md#the-fallback-repository) on the tracking Integration to give
those findings a tracking issue somewhere visible; without one they are reachable only through `kubectl` and the status
page.

A finding whose lookup _failed_ — throttling, an identity binding still propagating, a transient inventory outage — is
held and retried instead, because "could not find out" must not be confused with "no repository exists". The hold is
bounded by the accumulation window; past that the finding advances anyway, since a finding a human could be looking at
beats one held out of sight.
