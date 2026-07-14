# Patchy

An end-to-end workflow for triaging and remediating security findings from multiple sources using GitHub issues as a
state machine.

## Key Requirements

1. Initial solution will get finding reports from GitHub Advanced Security (namely CodeQL findings), but the solution
   should be tool-agnostic using some sort of plugin architecture or reusable library.
   - use github webhooks to receive code scanning alerts / findings
   - retrieve the alert contents and open a new github issue containing all of the relevant contextual information
   - use a consistent label to identify the unique finding number
   - use a consistent label to identify the source
   - use a consistent label to identify the CVE/CWE/GHSA identifier (categorization)
2. Multiple findings targeting the same finding type (CVE/CWE/GHSA, etc) against the same repository should be
   accumulated for up to 1 hour.
3. Once at least an hour has passed, update the github issue label to allow a coding agent to pick up the issue and
   attempt to remediate (more on that below)
4. A separate webhook receiver should exist to pick up any of these newly created issues to enhance the context
   - may pull information from CMDB to gather ownership / associated infrastructure relationships -- this should be
     injected into the issue; likely as a separate comment since the main issue body will be owned by the accumulator
   - update an issue label to identity that the issue has successfully been augmented
   - note that this enhancement logic has not been finalised, so this should be a logic "placeholder" for now, but the
     code required to pick up an issue and update its state should all be created now
5. A coding agent should only pickup issues that are older than 1 hour and are in an appropriate state (context has been enhanced)
   - pick up issues also via webhook
   - coding agent runtime should download the issue contents into a consistent markdown file (templated)
   - coding agent should clone the current main branch of the repository
   - coding agent runtime should then bootstrap claude-code (using claude -p) with an initial prompt to determine if the
     issue is a false positive and the likelihood that it can be remediated successfully
     - expose the issue markdown
     - expose the cloned repository
     - no internet access / no access to github APIs will be granted to claude itself
   - agent should write out its summary in a markdown file. The metadata should be contained in parseable yaml
     frontmatter and contain the following:
     - recommendation: ignore (false positive), remediate (agentic remediation), manual (human remediation)
     - priority: low, medium, high
     - severity: low, medium, high
     - confidence: a value between 0-1 that describes how confident you are in the recommendation (for remediate, the likelihood of success)
     - confidence in remediation should be based on the likelihood that the issue can be fully remediated without breaking functionality
     - Always favor solutions that are backwards compatible (do not require breaking changes to external callers)
     - If a better solution exists that requires breaking changes, include that information in the summary and wait for a human to
       approve the worse solution (/approve comment)
     - if recommend remediation:
       - model - what model should be used to remediate?
       - budget - recommended token budget / max turns before giving up
6. Coding agent runtime should parse the markdown frontmatter and determine what to do next:
   - Always update the issue with the summary and update labels / issue fields (priority, severity) accordingly
   - If false positive, dismiss the finding in GHAS and close the issue
   - If manual: assign the issue to the owner of the repository for triage
   - If remediate and confidence < 0.75, assign the issue to the owner of the repository
     - provide instructions for commenting on the issue to force remediation attempt (/approve comment)
   - If remediate and confidence > 0.75, set the issue label for the state and continue with step 7; otherwise, stop
7. Coding agent runtime issues a new prompt (using claude -p) to attempt to remediate the issue
   - expose the original issue markdown
   - expose the cloned repository
   - expose the report summary from the first stage (classification)
   - set the token budget / max turns to ensure that the coding agent does not burn through infinitely
   - coding agent should create:
     - summary report of whether or not the issue could be remediated and how it was remediated
       - use frontmatter to indicate success
       - use frontmatter to identify confidence score (0-1) on how confident we are that the issue has been remediated
     - commit.sh script used to commit the changeset against the repository containing the remediation
8. coding agent runtime should parse the remediation report frontmatter to determine what to do next:

- if remediated, run commit.sh against a feature branch and open a pull request
- update the original issue to set status "in-review"
- merging the PR should be left to humans, but once merged, the issue should be automatically closed

## Architecture

Use golang for each of the components. Maintain separation of concerns (not a monolithic solution)

Steal code from evolve (@../evolve/) for the coding agent execution (namely the harness invocation). We are starting with
claude, but may also support other harnesses in the future. The harness used for classification may be different than the
harness used for remedation, so keep that configurable.

Ensure that the issue, the classification report, and the remediation report are templated / consistent across the estate.

Assume that the components will be hosted in Kubernetes clusters
Ensure that the components support opentelemetry for logging, tracing, and metrics
use structured logging (slog)

See golang skills (golang-style, golang-project, etc).

May use etcd / kubernetes controllers as a state machine if absolutely necessary, but the source of truth should be
github issues.

## Recommended Labels

### set by source "plugin"

```txt
security-source: "ghas" # the source of the finding
security-advisory: <CWE#,CVE#,GHSA#> # the advisory / finding type number
security-finding: "opened"
```

### set by the context enhancement component

```txt
security-finding: "context-enhanced"
```

### set by coding agent runtime classifier

```txt
security-finding: "classifying|classified" # set to classifying before submission, classified after
security-classifier: "claude"
security-severity: "low|medium|high|critical"
security-priority: "low|medium|high|critical"
security-recommendation: "remediate|ignore|manual"
security-recommendation-confidence: "<confidence score>"
security-token-budget: "<output-token-budget>" # only set if recommendation is to remediate
security-max-turns: "<max-turns-to-allow>" # only set if recommendation is to remediate
security-classification-input-tokens: "<input-token-count>"
security-classification-output-tokens: "<output-token-count>"
security-classification-turns: "<number of turns>"
security-classification-cost: "<cost in USD>"
security-classification-session: "<session identifier>"
security-classification-elapsed: "<remediation-elapsed-time>"
```

### set by the coding agent runtime

```txt
security-finding: "remediated|attempted" # set to remediated if successful, attempted otherwise
security-recommendation: "manual" # only set if remediation was not successful
security-remediator: "claude"
security-remediation-input-tokens: "<input-token-count>"
security-remediation-output-tokens: "<output-token-count>"
security-remediation-turns: "<number of turns>"
security-remediation-cost: "<cost in USD>"
security-remediation-session: "<session identifier>"
security-remediation-elapsed: "<remediation-elapsed-time>"
```

## Recommended Components

1. source-controller

- Retrieves or receives findings from a source (SAST tools, agentic findings)
- Opens new github issues
- acts as an accumulator based on the security-advisory label
- accumulation for up to 1 hour from first issue with a given security-advisory label, then opens a new one
- should be a package with an interface to implement to make it easy to add additional sources in the future without
  duplicating tons of code

1. context-controller

- reacts to security-finding: "opened" issues on creation
- enhances the issue context from 1st/3rd parties (plugin architecture?)
- updates issue state to "security-finding": "context-enhanced"

1. remediation-controller

- spin up classification / remediation coding agent ephemeral container
- run both classification / remediation in the same container context to avoid multiple repository clones
- runs coding agent in isolation (no network access)
- acts as intermediary between github issues and coding augments; performs all tasks against github api
  - cloning repositories
  - creating commits and opening pull requests
  - updating issues

1. Coding agent runtime

- may or may not be baked into the remediation controller
- shared harness / prompts / skills execution runtime (used to invoke coding agents with pre-defined prompts / expectations)

Follow our conventions for golang: see @../evolve/ and @../dotty/ for examples. IF we use kubernetes controllers, follow
the conventions used by fluxcd (see <https://github.com/fluxcd/flux2> and <https://github.com/fluxcd/source-controller>)
