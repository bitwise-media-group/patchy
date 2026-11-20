# Resolving a repository

Every pipeline stage after triage needs a SHA-pinned repository to work in. A cloud finding names no repository, because
it is about a **resource**, not a file. The context enhancers fill that gap. Each one:

1. looks up the finding's resource in its cloud's inventory
2. copies the resource's metadata onto the finding as attributes
3. finds the owning repository from **ownership labels or tags on the resource itself**

Every enhancer in this section follows one rule: it acts on the finding's cloud resource only, never on which source
ingested the finding. A Google Cloud resource gets the [Google Cloud Asset Inventory lookup](google-cai.md) whether
[SCC](../sources/google-scc.md) or [Wiz](../sources/wiz.md) raised the finding. An AWS or Azure resource gets its
[tags](aws.md) [lookup](azure.md) the same way. This includes cloud findings from a
[generic source](../sources/generic.md).

The chain runs in the context-controller in a fixed order: the cloud lookups, then the
[generic HTTP fan-out](generic.md), then the static context file. Each cloud lookup and generic call is skipped unless
an Integration turns it on; the static file is skipped unless the context-controller's `--static-context-file` flag
names one. [context-controller](../../configuration/context-controller.md) describes how the chain behaves at runtime.

## The ownership labels

--8<-- ".snippets/ownership-labels.md"

The URL form takes priority over the triple, and it is the only form that can name a self-hosted forge. The vocabulary
is the same on every cloud. Only the spelling rules differ:

| Cloud        | Spelled as      | Read from                       | Can values carry `://`?                                                                                            |
| ------------ | --------------- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| Google Cloud | resource labels | Cloud Asset Inventory           | No. Label values are lowercase, 63 characters; the scheme is added when absent. Security marks have no such limit. |
| AWS          | resource tags   | AWS Config or Resource Explorer | Yes, as written. The scheme is still optional, so one vocabulary can be shared across clouds.                      |
| Azure        | resource tags   | Azure Resource Graph            | Yes, as written. The scheme is still optional.                                                                     |

You can change the key names per provider if you already have a naming convention. Use `cloudAssetInventory.labels` on
the `google-cloud` Integration, and `resourceTags.tags` on `aws` and `azure`.

## When no repository resolves

Most resources carry no ownership labels or tags, and that is not a failure. Neither is a resource that the inventory
has no record of. Such a finding is still ingested, keeps its enrichment attributes and still reaches a human. It cannot
be remediated automatically, so the investigation gate hands it off. Set
[`github.issues.fallbackRepository`](../sources/github.md#the-fallback-repository) on the tracking Integration to give
those findings a tracking issue somewhere visible. Without it, you can reach them only through `kubectl` and the status
page.

A finding whose lookup _failed_ is held and retried instead. Causes include throttling, an identity binding that is
still propagating, or a short inventory outage. Patchy must not treat "could not find out" as "no repository exists".
The hold lasts at most the accumulation window. After that the finding moves on anyway, because it is better for a human
to see the finding than for it to stay hidden.
