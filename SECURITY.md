# Security Policy

## Reporting a vulnerability

Please report vulnerabilities privately via
[GitHub Security Advisories](https://github.com/bitwise-media-group/patchy/security/advisories/new). Do not open public
issues for security reports.

## Threat model (summary)

patchy is a set of Kubernetes services that receive GitHub webhooks, mutate GitHub issues/PRs, and run a coding agent
against cloned repositories inside ephemeral Jobs. Its security surface is webhook authenticity, GitHub credential
scope, and the isolation of the agent from the code it operates on. It defends against:

- **Forged webhooks** — every delivery is validated against the shared webhook secret (HMAC-SHA256, constant-time
  compare) before any payload is parsed.
- **Credential exposure to the agent** — the coding agent container receives no GitHub credentials at all; repository
  clones are performed by a controller-minted, short-lived, single-repository token that exists only in the Job's init
  container environment. Pod egress is restricted so the agent can reach its model API and nothing else.
- **Agent-authored outputs** — reports and commit scripts written by the agent are validated (schema-checked
  frontmatter, model allowlists, clamped budgets) and executed only inside the disposable Job pod, never on a
  controller; repository state is verified rather than trusting agent claims.
- **Tampered release artifacts** — releases ship `checksums.txt`, a SLSA build-provenance attestation, keyless Sigstore
  (cosign) bundles per binary, and an SPDX SBOM per archive.

Out of scope: the repositories patchy operates on and the model provider itself; a compromise of the release workflow's
signing identity; and a compromise of the cluster patchy runs in.

## Code scanning triage

CodeQL findings are triaged in
[`security/code-scanning/index.md`](security/code-scanning/index.md), with a report per finding
recording why it was dismissed or how it was remediated.
