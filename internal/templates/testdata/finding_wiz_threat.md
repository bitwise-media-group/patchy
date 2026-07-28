A service account key was used from an unusual location.

### Affected resource

| | |
| --- | --- |
| **Resource** | `//compute.googleapis.com/projects/acme-prod/zones/europe-west2-a/instances/build-vm` |
| **Name** | build-vm |
| **Type** | `compute#instance` |
| **Platform** | GCP |
| **Account** | `acme-prod` |
| **Region** | europe-west2-a |

### Detection

| | |
| --- | --- |
| **Rule** | Unusual key usage (`dr-5678`) |
| **Wiz severity** | critical |
| **Status** | OPEN |
| **Detections** | 2 |
| **Created** | 2026-07-26T10:00:00Z |
| **Wiz** | [view threat](https://app.wiz.io/threats#~(threat~'t-0001')) |

### Actors

- ci-deployer@acme-prod.iam (service_account)


### MITRE ATT&CK

Tactics: `TA0001` — techniques: `T1078`

### Cloud accounts

- `acme-prod`


---
*Threat `t-0001`, reported by Wiz Defend. Runtime detections route to a
human for response; patchy tracks them but does not auto-remediate live
threats.*
