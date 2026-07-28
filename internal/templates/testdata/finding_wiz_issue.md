The bucket allows public read access.

### Affected resource

| | |
| --- | --- |
| **Resource** | `//storage.googleapis.com/acme-artifacts` |
| **Name** | acme-artifacts |
| **Type** | `storage#bucket` |
| **Platform** | GCP |
| **Account** | `acme-prod` (Acme Production) |
| **Region** | europe-west2 |
| **Console** | [view in cloud console](https://console.cloud.google.com/storage/browser/acme-artifacts) |

### Issue

| | |
| --- | --- |
| **Control** | Bucket accessible to the public (`wc-id-1234`) |
| **Wiz severity** | high |
| **Status** | OPEN |
| **Projects** | acme-prod |
| **Created** | 2026-07-26T09:00:00Z |
| **Wiz** | [view issue](https://app.wiz.io/issues#~(issue~'f2f5d3b8')) |

### Recommended resolution

Remove allUsers from the bucket IAM policy.

### Resource tags

| Tag | Value |
| --- | --- |
| `env` | prod |
| `team` | storage |

---
*Issue `f2f5d3b8-a663-4c1b-b7f3-8f7f0c8a0001`, reported by Wiz.*
