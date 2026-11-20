# Benchmarks & load tests

Patchy is sized for organizations that already have a large backlog. Onboarding an organization with a **2M-finding
backlog** must not overload the controllers. Two opt-in suites check this. Neither runs in `make pr` or CI:

- **Tier 1, microbenchmarks** (`mise run bench`): per-package Go benchmarks over deterministic synthetic datasets
  (`internal/loadgen`). Pure in-memory paths (scheduler picking, rollup arithmetic) run at the full 2M. Paths backed by
  custom resources (CRs) run at 1k, 10k and 100k. Complexity-ratio assertions prove their cost stays flat, and the
  results extrapolate to 2M.
- **Tier 2, load tests** (`mise run load`): an envtest kube-apiserver, the **real controller binaries**, and 50–100k
  findings driven through them. 2M real custom resources do not fit in etcd (a 2–8GB quota at 1.5–20KB+ per finding). So
  Tier 2 checks the Tier-1 extrapolation at the largest size the harness supports.

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

**Ratios are asserted; absolute numbers are reported.** Hard failures come only from _complexity ratios_, which give the
same result on any machine. For example, "the per-alert cost at a 100k backlog is at most 3× the cost at 1k" fails the
same way on any hardware. Absolute numbers (alerts/s, request latency, RSS) depend on the machine. So the suites print
them as a report for you to compare with the targets below, and never assert them.

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

Etcd cannot hold 2M findings. So the claim "patchy handles 2M" breaks down into three parts:

1. **Every hot path is measured to be flat (or n log n) in the backlog size** from 1k to 100k, two orders of magnitude.
   The complexity assertions check this. A path that stays flat from 1k to 100k because it uses index lookups instead of
   scans also stays flat at 2M. The underlying structures (hash indexes, field indexers, the API server's own storage)
   do not change behavior past 100k.
2. **Paths that are pure arithmetic run at the actual 2M** (scheduler `Pick` over 2M candidates, the 2M dataset build
   behind `PATCHY_BENCH_HUGE`). Nothing there is extrapolated.
3. **Memory grows linearly**: the informer caches use most of it, at ~1–2KiB per lean finding (managedFields stripped).
   100k findings ≈ 150–300MiB per controller, and 2M ≈ 3–6GiB. Size the controller limits to your backlog (see the
   comment in `charts/patchy/values.yaml`). Delayed-queue entries (accumulation windows, TTL requeues) add ~150–250B
   each, up to ~500MiB at 2M. That is real memory, but the cache itself is much larger.

Keeping these paths flat took the following changes, all of which landed with this suite:

- the key-hash **field index** behind ingest's family lookup. The old per-alert label-selector scan was O(backlog). The
  fold ratio measured **8.56×** before and **≤1.35×** after.
- the phase field index behind the gate's Forge fan-out
- generation predicates on the Forge watches
- bounded, parallel enhancer fan-out with a config cache that expires on a TTL
- compact, gzipped dataset responses assembled with index sorts
- managedFields stripped from every informer cache

## The rollup drain ceiling (documented, not fixed)

Every terminal finding and every completed run folds into the single `total` FindingRollup object, retrying on conflict.
This single object is an **intentional serialization point**. Using one object with a 512-entry ledger is what makes the
statistics exactly-once. It also caps terminal drain at roughly **50–100 findings/s**. Each fold is a read-modify-write
API round-trip, and contention adds retries. At that rate, a 2M-finding backlog drains through terminal states in ~6–11
hours. That is acceptable, because investigation throughput (agent runs) limits the pipeline long before the ledger
does. `TestLoadRollups` measures the actual rate. The `rollup-finding` controller also filters out watch events that
provably cannot change the rollup, so findings still in flight cost it nothing. Revisit this only if terminal drain ever
needs to go faster than ~100/s. The fix then is to shard the `total` scope. A faster retry loop would not help.

## What the load tests cannot see

envtest has no kubelet. Agent Jobs are created but never run, so the tests fake pipeline progress past ingest with
status writes, the same way the rest of the e2e suite does. The schedulers' concurrency caps
(`--max-concurrent-investigations`, `--max-concurrent-remediations`) limit agent throughput, and data-structure scale
does not. So agent throughput is out of scope here. Informer sync time at 100k adds 5–15s to every controller's cold
start. The load tests report it separately from steady-state numbers.
