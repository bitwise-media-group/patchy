GitHub secret scanning found a **slack_api_token** committed to this repository.

**Rotate or revoke this credential first.** Removing it from the code does not make it safe: it stays in the git
history, in forks and clones, and in any copy taken while it was exposed. Treat the credential as compromised until the
issuer has invalidated it. A cleanup change that removes the literal from the working tree comes second, and patchy only
opens one after a human approves it.

### Secret

| | |
| --- | --- |
| **Type** | `slack_api_token` |
| **Validity** | unknown |
| **Publicly leaked** | no |
| **Found in other repositories** | no |
| **Push protection bypassed** | no |

---
*Reported by GitHub secret scanning. The secret value is never copied into patchy.*
