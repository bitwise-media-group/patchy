# source-controller

Receives `code_scanning_alert` webhooks, opens issues, and accumulates related alerts into them. It owns the
`security-finding: opened` initial state and the whole `security-accumulation` lifecycle.

```sh
source-controller serve --webhook-secret-file /etc/patchy/webhook/secret \
  --github-app-id 123456 --github-app-private-key-file /etc/patchy/github-app/private-key.pem
```

## Flags

All [shared flags](index.md#shared-flags-all-three-controllers), plus:

<div class="nowrap-first" markdown>

| Flag                    | Env                          | Default | Purpose                                                            |
| ----------------------- | ---------------------------- | ------- | ------------------------------------------------------------------ |
| `--accumulation-window` | `PATCHY_ACCUMULATION_WINDOW` | `1h`    | How long alerts of one finding type accumulate into a single issue |

</div>

## Behavior

- **New finding** — if no open-accumulation issue exists for `(repository, source, primary advisory)`, open one with the
  full initial label set. The primary advisory identifier (GHSA over CVE over the most specific CWE) keys the window.
- **Accumulation** — a finding matching an open-accumulation issue younger than the window folds into it: the body's
  machine manifest grows and a `security-alert: <n>` label is added. No state changes.
- **Window close** — the reconcile pass (every `--reconcile-interval`) flips issues whose age exceeds the window from
  `security-accumulation: open` to `complete`, add-before-remove so an accumulation label is always present. Alerts
  arriving after the flip open a fresh issue.

!!! note "Tuning the window"

    The remediation-controller's `--issue-min-age` (default `1h`) should be at least the accumulation window —
    pickup requires `accumulation: complete`, and the two defaults are deliberately equal. The dev overlay drops
    both to `2m` for fast loops.
