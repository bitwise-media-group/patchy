<!-- patchy:finding patchy/finding-abc123def0-1 -->

## Security finding

Reflected cross-site scripting

| | |
| --- | --- |
| **Source** | `github-code-scanning` |
| **Repository** | [acme/shop](https://github.com/acme/shop) |
| **Severity** | high |
| **Rule** | `js/reflected-xss` |
| **Advisories** | `CWE-79`, `CVE-2026-1234` |
| **Phase** | Opened |

### Description

Directly writing user input to the page allows XSS.

Sanitize all user input.

### Alerts

- [`7`](https://github.com/acme/shop/security/code-scanning/7) — `src/render.js:42`


---
*This issue is a read-only projection of the `finding-abc123def0-1` Finding; patchy re-renders it as the finding progresses.*
