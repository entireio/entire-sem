# Benchmarks

Reproducible performance and quality measurement for `entire-sem`, per v2-plan
WP10. The harness lives in `cmd/sem-bench` (driver) and `internal/bench`
(measurement core); see `bench/README.md` for layout and flags.

## Running

```sh
# Pin repo commits once (writes bench/repos.lock.json) — commit the result.
go run ./cmd/sem-bench -update-lock

# Fast tier — routine per-phase tracking (minutes):
go run ./cmd/sem-bench -manifest bench/repos.fast.json

# Full tier — all 24 languages x 10 repos (slow; includes mega-repos):
go run ./cmd/sem-bench

# Quick subset / offline:
go run ./cmd/sem-bench -languages Go,Rust -limit 3
go run ./cmd/sem-bench -skip-clone
```

Cloning (network) is a distinct phase from measurement, which runs the provider
with `NoNetwork` set, so the measured path is the same local-only path the
provider guarantees in production. Cloned repos live under `bench/.cache/`
(gitignored) and never enter our commits.

## Comparing across phases

Both tiers pin commits via `bench/repos.lock.json`, so the source under analysis
is fixed and only the analyzer changes between runs. To compare two work phases:

1. Check out phase A, run a tier, keep its `bench/results/result-*.json`.
2. Check out phase B, run the same tier.
3. Diff the `by_language` and `totals` aggregates.

Quality regressions show up as shifts in the resolution/confidence distributions
or rising parse failures; performance regressions show up as falling LOC/sec or
rising wall time.

## Metrics

Per repository (and aggregated per language and overall):

- **Performance:** wall time, files, lines of code, files/sec, LOC/sec, output
  bytes, Go allocation bytes.
- **Quality:** symbols, relations by type, resolution distribution
  (`exact`/`import_resolved`/`type_inferred`/`name_only`/`pattern`), confidence
  bands (`exact`/`strong`/`heuristic`/`weak`), parse-failure codes, unresolved
  relative imports.

## Findings to date

The harness has already driven concrete changes:

- **Route over-firing.** An early run showed gin emitting 1039 `HANDLES_ROUTE`
  edges — every path-like string literal counted as a route. Requiring routing
  context on the literal's line cut that to 206 (real registrations only).
- **C/C++ throughput is the floor.** C/C++ parse at ~1.5–3.5k LOC/s versus
  ~10–23k LOC/s for Go and scripting languages, and the C/C++ repos are the
  largest, so they dominate full-tier wall time.
- **Peak memory scaled with repo size.** The in-memory snapshot accumulates
  every relation with its evidence, reaching ~20 GB RSS on tensorflow. The
  streaming snapshot path (`StreamSnapshot`, used by the CLI) emits records as
  produced and never holds the full relation set, so peak memory no longer
  scales with the relation count.

## Notes

- A full-tier run is heavy (the manifest includes linux, tensorflow, vscode,
  etc.); use the fast tier for routine tracking and the full tier occasionally.
- `internal/bench` is unit-tested (`MeasureRepo`, `BuildReport`) and needs no
  network, so the measurement logic is covered in CI; only the clone phase
  touches the network.
