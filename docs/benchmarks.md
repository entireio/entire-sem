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

For long runs, add `-progress` to print provider phase telemetry to stderr
without changing the JSON report. Guardrails can make local or CI runs fail
after writing the report:

```sh
go run ./cmd/sem-bench -profile syntax-only -languages Go -limit 1 \
  -min-loc-per-sec 50000 -max-rss-bytes 1000000000
```

### Per-profile examples

Each profile measures the production streaming path at a different depth. Small
or medium runs make the trade-off visible:

```sh
# syntax-only — fastest; symbol inventory + structure only.
go run ./cmd/sem-bench -profile syntax-only -languages Go -limit 3

# fast — symbols, imports, shallow calls, boundaries, IaC; no deep relations.
go run ./cmd/sem-bench -profile fast -languages Go,Python -limit 5

# full — the complete relation graph (default).
go run ./cmd/sem-bench -profile full -languages Go,Python,TypeScript -limit 5
```

Read speed/throughput numbers from `fast` (and `syntax-only` for the floor), and
semantic-depth/coverage numbers from `full`.

Cloning (network) is a distinct phase from measurement, which runs the provider
with `NoNetwork` set, so the measured path is the same local-only path the
provider guarantees in production. The measured path is `StreamSnapshot` (the
production streaming path, memory-bounded), not the in-memory accumulator, so
large-repo runs do not OOM. Cloned repos live under `bench/.cache/` (gitignored)
and never enter our commits.

Pass `-profile full|fast|syntax-only` to measure a given indexing depth (default
`full`); the report records the profile, hardware (OS/arch/CPUs), and process
peak RSS.

The provider CLI also accepts `--progress` on `snapshot`, `symbols`, and
`edges`. Progress lines are written to stderr and include phase, file/symbol/
relation counts, heap, RSS, and elapsed time; stdout remains valid NDJSON.

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

Every report includes the profile, hardware (OS/arch/CPUs), process peak RSS,
provider version, and schema version at the run level, and per repository the
relation set, languages, files/LOC, wall time, and output size. Full breakdown:

Run-level: profile, hardware (OS/arch/CPUs), process peak RSS, provider version,
schema version. Per repository (and aggregated per language and overall):

- **Performance:** wall time, files, lines of code, files/sec, LOC/sec, output
  bytes (of the streamed NDJSON), Go allocation bytes, profile, relation set.
- **Quality:** symbols, relations by type, resolution distribution
  (`exact`/`import_resolved`/`type_inferred`/`name_only`/`pattern`), confidence
  bands (`exact`/`strong`/`heuristic`/`weak`), parse-failure codes, unresolved
  relative imports.

The streaming path's only relation-count-scaled memory is the dedup set (one
compact 64-bit key per unique relation), so its entry count equals the reported
unique `relations` total — no separate dedup-count metric is emitted because
`relations` already measures it.

## Findings to date (historical, pre-streaming)

These observations come from **early runs that measured the in-memory path,
before the benchmark measured `StreamSnapshot` and before profiles existed**.
Treat the numbers as historical; re-run with the current streaming benchmark
(and a named profile) for current figures.

- **Route over-firing.** An early run showed gin emitting 1039 `HANDLES_ROUTE`
  edges — every path-like string literal counted as a route. Requiring routing
  context on the literal's line cut that to 206 (real registrations only).
- **C/C++ throughput used to be the floor.** Early C/C++ runs parsed at
  ~1.5–3.5k LOC/s because HEAD snapshots spawned `git show` once per file and
  C/C++ field symbols were emitted even though C/C++ field-access relations are
  not consumed. Current HEAD snapshots use one `git cat-file --batch` process,
  syntax-only skips relation-resolution indexes, oversized generated files emit
  `E_FILE_TOO_LARGE` instead of being parsed, and C/C++ field symbols are
  suppressed. A Linux syntax-only run on this branch measured ~235k LOC/s over
  38.2M LOC.
- **Syntax-only memory is compacted separately.** The syntax-only profile only
  emits `DEFINES` and `CONTAINS`, so it does not need to retain full
  `SymbolRecord` payloads after those records are streamed. A follow-on memory
  run over Linux kept the same 3.37M symbols / 3.37M relations while reducing
  peak RSS from ~5.82 GB to ~1.62 GB by retaining only structural symbol
  metadata for phase 2.
- **Peak memory scaled with repo size.** The in-memory snapshot accumulated
  every relation with its evidence and source contents, reaching ~20 GB RSS on
  tensorflow. The streaming path (`StreamSnapshot`, now the benchmark's measured
  path) emits records as produced and no longer holds full relation payloads,
  their evidence, or file contents in memory. Peak memory is instead bounded by
  the symbol/index metadata plus a compact relation dedup set (one 64-bit key
  per unique relation). That dedup set still scales with the count of unique
  relations — it is the remaining relation-count-scaled component — but at a
  tiny constant per relation rather than the full payload, which is what kept
  the in-memory path from finishing on the largest repos.

## Notes

- A full-tier run is heavy (the manifest includes linux, tensorflow, vscode,
  etc.); use the fast tier for routine tracking and the full tier occasionally.
- `internal/bench` is unit-tested (`MeasureRepo`, `BuildReport`) and needs no
  network, so the measurement logic is covered in CI; only the clone phase
  touches the network.
