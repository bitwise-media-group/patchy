# Security Policy

## Reporting a vulnerability

Please report vulnerabilities privately via
[GitHub Security Advisories](https://github.com/bitwise-media-group/patchy/security/advisories/new). Do not open public
issues for security reports.

## Threat model (summary)

patchy is a set of Kubernetes services. They receive security alerts from code scanners and cloud security tools, open
and update GitHub issues and pull requests, and run a coding agent against a copy of each repository inside short-lived
Jobs. The security surface is the authenticity of incoming alerts, the scope of the GitHub credentials, and the
isolation of the agent. patchy defends against:

- **Forged webhooks.** Every delivery is authenticated before any payload is parsed: an HMAC-SHA256 signature checked
  with a constant-time compare (GitHub and generic integrations), a Google-signed OIDC token (Pub/Sub push), or a bearer
  token (Wiz). Each Integration has its own secret.
- **Credential exposure to the agent.** The agent pod holds no credential of any kind. Its init container downloads the
  repository as a SHA-pinned tarball from source-controller's in-cluster artifact server and checks its digest. It needs
  no GitHub token to do so. Model traffic goes through the egress broker, which adds the model credential to each
  request outside the pod. With a hostname-aware network policy (Cilium, GKE or Istio) the pod can reach only DNS, the
  artifact server and the broker. A plain NetworkPolicy also allows TCP 443 out, but the pod has no credential to use
  there.
- **Agent-written output.** Reports and commit scripts written by the agent are validated (schema-checked frontmatter, a
  model allowlist, budgets clamped to the configured limits). Commit scripts run only inside the disposable Job pod,
  never on a controller. The controllers check repository state instead of trusting what the agent says.
- **Tampered release artifacts.** Releases ship `checksums.txt`, a SLSA build-provenance attestation, keyless Sigstore
  (cosign) bundles for each linux and windows binary, Developer ID-signed and Apple-notarized darwin binaries, and an
  SPDX SBOM for each binary.

Out of scope: the repositories patchy operates on and the model provider itself; a compromise of the release workflow's
signing identity or of the organisation's Apple Developer ID certificate; and a compromise of the cluster patchy runs
in.

## Code scanning triage

CodeQL findings are triaged in [`security/code-scanning/index.md`](security/code-scanning/index.md), with a report per
finding recording why it was dismissed or how it was remediated.
