The bucket is publicly readable.

### Affected resource

| | |
| --- | --- |
| **Resource** | `//storage.googleapis.com/projects/acme-prod/buckets/acme-artifacts` |
| **Name** | acme-artifacts |
| **Type** | `google.cloud.storage.Bucket` |
| **Project** | `projects/acme-prod` |
| **Location** | europe-west2 |
| **Service** | storage.googleapis.com |

### Finding

| | |
| --- | --- |
| **Category** | `PUBLIC_BUCKET_ACL` |
| **Class** | MISCONFIGURATION |
| **SCC severity** | high |
| **CVE** | `CVE-2026-1234` (CVSS 7.5) |
| **Detected** | 2026-07-26T09:00:00Z |
| **Console** | [view in Security Command Center](https://console.cloud.google.com/security/command-center/findings?f=abc123) |

### Recommended next steps

Remove allUsers from the bucket IAM policy.

### MITRE ATT&CK

Tactic: `IMPACT` — techniques: `T1485`, `T1486`

### Compliance

- **cis 1.3** — 5.1, 5.2

### Detector properties

| Property | Value |
| --- | --- |
| `ExceptionInstructions` | Add the mark |
| `Recommendation` | Restrict access |

### Security marks

| Mark | Value |
| --- | --- |
| `scm-repository-name` | infra-prod |
| `scm-repository-org` | acme |

---
*Finding `organizations/1234567890/sources/555/findings/abc123`, reported by Google Cloud Security Command Center.*
