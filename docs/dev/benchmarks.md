# Benchmarks & load tests

Patchy is sized for brownfield estates: onboarding an organization with a **2M-finding backlog** must not melt the
controllers. Two opt-in suites keep that promise honest — neither runs in `make pr` or CI:

- **Tier 1 — microbenchmarks** (`mise run bench`): per-package Go benchmarks over deterministic synthetic datasets
  (`internal/loadgen`). Pure in-memory paths (scheduler picking, rollup arithmetic) run at the full 2M; CR-backed paths
  sweep 1k/10k/100k and prove flatness with complexity-ratio assertions that extrapolate to 2M.
- **Tier 2 — load tests** (`mise run load`): an envtest kube-apiserver, the **real controller binaries**, and 50–100k
  findings driven through them. 2M real custom resources do not fit etcd (2–8GB quota at 1.5–20KB+ per finding), so Tier
  2 validates the Tier-1 extrapolation at the largest size the harness supports.

## Running

```sh
mise run bench                          # benchmarks + complexity assertions, recorded under coverage/bench/
PATCHY_LOAD_N=10000 mise run load       # load suite smoke; default N is 50000
```

Knobs:

| Variable              | Applies to | Meaning                                                                        |
| --------------------- | ---------- | ------------------------------------------------------------------------------ |
| `PATCHY_BENCH_ASSERT` | bench      | Arms the `TestComplexity*` ratio assertions (`hack/bench.sh` sets it)          |
| `PATCHY_BENCH_HUGE`   | bench      | Unlocks the 2M in-memory dataset build (~16GB; run with `-benchtime=1x`)       |
| `PATCHY_LOAD`         | load       | Gates the whole load suite (`hack/load.sh` sets it)                            |
| `PATCHY_LOAD_N`       | load       | Findings per load test (default `50000`; budget ~15min at 50k, ~35min at 100k) |
| `PATCHY_LOAD_ASSERT`  | load       | Arms the load suite's ratio assertion (the ingest decile check)                |

Every run is teed into `coverage/bench/` (git-ignored). Compare two runs with benchstat (pinned in `mise.toml`):

```sh
benchstat coverage/bench/<before>.txt coverage/bench/<after>.txt
```

## Pass semantics

**Ratio-asserted, absolute-reported.** Hard failures fire only on machine-portable _complexity ratios_ — "the per-alert
cost at a 100k backlog is at most 3× the cost at 1k" fails the same way on any hardware. Absolute numbers (alerts/s,
request latency, RSS) depend on the machine, so they are printed as a report to be read against the targets below, never
asserted.

## Targets

| Measurement                                        | Target          | Checked by                                            |
| -------------------------------------------------- | --------------- | ----------------------------------------------------- |
| Ingest fold cost ratio, 100k ÷ 1k backlog          | ≤ 3×            | `TestComplexityIngestFold` (asserted)                 |
| Ingest per-alert cost, last ÷ first decile at load | ≤ 2.0           | `TestLoadIngest` (asserted with `PATCHY_LOAD_ASSERT`) |
| Sustained ingest at 100k existing findings         | ≥ 500 alerts/s  | `TestLoadIngest` (reported)                           |
| Dataset build per-finding cost ratio, 100k ÷ 10k   | ≤ 1.3×          | `TestComplexityBuildDataset` (asserted)               |
| `GET /api/findings` at 100k findings               | ≤ 5s            | `TestLoadStatusServer` (reported)                     |
| integration-controller peak RSS at 100k findings   | ≤ 2GiB          | `TestLoadIngest` (reported)                           |
| Scheduler pick cost ratio, 2M ÷ 100k pending       | ≤ 30× (n log n) | `TestComplexityPick` (asserted)                       |

## The 2M extrapolation method

Etcd cannot hold 2M findings, so the claim "patchy handles 2M" decomposes into:

1. **Every hot path is measured to be flat (or n log n) in the backlog size** across 1k → 100k, two orders of magnitude.
   The complexity assertions encode this; a path that is flat across 1k→100k because it does index lookups instead of
   scans stays flat at 2M, because the underlying structures (hash indexes, field indexers, the API server's own
   storage) do not change regime past 100k.
2. **Paths that are pure arithmetic run at the actual 2M** (scheduler `Pick` over 2M candidates, the 2M dataset build
   behind `PATCHY_BENCH_HUGE`), so nothing there is extrapolated at all.
3. **Memory extrapolates linearly**: the informer caches dominate, at ~1–2KiB per lean finding (managedFields stripped).
   100k findings ≈ 150–300MiB per controller; 2M ≈ 3–6GiB — size controller limits to the backlog (see the comment in
   `charts/patchy/values.yaml`). Delayed-queue entries (accumulation windows, TTL requeues) add ~150–250B each,
   worst-case ~500MiB at 2M — real, but dominated by the cache itself.

What flatness required (all landed with this suite): the key-hash **field index** behind ingest's family lookup (a
per-alert label-selector scan was O(backlog): the fold ratio measured **8.56×** before, **≤1.35×** after), the phase
field index behind the gate's Forge fan-out, generation predicates on the Forge watches, bounded+parallel enhancer
fan-out with a TTL'd config cache, compact/gzipped dataset responses assembled with index sorts, and managedFields
stripped from every informer cache.

## The rollup drain ceiling (documented, not fixed)

Every terminal finding and every completed run folds into the single `total` FindingRollup object under conflict-retry.
That is a **deliberate serialization point** — one object is what makes the statistics exactly-once with a 512-entry
ledger — and it caps terminal drain at roughly **50–100 findings/s** (each fold is a read-modify-write API round-trip;
contention adds retries). At that rate a 2M-finding backlog drains through terminal states in ~6–11 hours, which is
acceptable because investigation throughput (agent runs) is the real bottleneck long before the ledger is.
`TestLoadRollups` measures the actual rate; the `rollup-finding` controller additionally filters watch events that
provably cannot change the rollup, so the in-flight population costs it nothing. Revisit only if terminal drain ever
needs to outrun ~100/s: the design answer is sharded scopes, not a faster retry loop.

## What the load tests cannot see

envtest has no kubelet: agent Jobs are created but never run, so pipeline progression past ingest is fabricated via
status writes, exactly like the rest of the e2e suite. Agent throughput is bounded by the schedulers' explicit
concurrency caps (`--max-concurrent-investigations`, `--max-concurrent-remediations`), not by data-structure scale, so
it is out of scope here. Informer sync time at 100k adds 5–15s to every controller's cold start — the load tests report
it separately from steady-state numbers.
