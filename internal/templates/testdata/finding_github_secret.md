GitHub secret scanning found a **GitHub Personal Access Token** committed to this repository.

**Rotate or revoke this credential first.** Removing it from the code does not make it safe: it stays in the git
history, in forks and clones, and in any copy taken while it was exposed. Treat the credential as compromised until the
issuer has invalidated it. A cleanup change that removes the literal from the working tree comes second, and patchy only
opens one after a human approves it.

### Secret

| | |
| --- | --- |
| **Type** | `github_personal_access_token` |
| **Provider** | GitHub |
| **Validity** | active |
| **Publicly leaked** | yes |
| **Found in other repositories** | yes |
| **Push protection bypassed** | yes |
| **Alert** | [view in GitHub secret scanning](https://github.com/acme/shop/security/secret-scanning/5) |

### Locations

- `config/app.yaml:4` (commit `abc123`)
- issue_comment: https://api.github.com/repos/acme/shop/issues/comments/1

…and 3 more location(s).

---
*Reported by GitHub secret scanning. The secret value is never copied into patchy.*
