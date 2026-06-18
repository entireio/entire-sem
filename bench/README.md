# Semantic Provider Benchmark

`sem-bench` clones popular real-world repositories per language and measures the
semantic provider over them, emitting a machine-readable performance and quality
report. It is the WP10 harness from the v2-plan and is meant to be re-run each
work phase to track regressions and quality gains.

## Design

- **Clone phase (network) is separate from measurement (no-egress).** Cloning
  uses `git`; the measured analysis runs `BuildProviderSnapshot` with
  `NoNetwork` set, so the path we benchmark is the same local-only path the
  provider guarantees in production.
- **Cloned repos never enter our commits.** They live under `bench/.cache/`,
  which is gitignored.
- **Repeatable.** `bench/repos.json` lists the repositories; `bench/repos.lock.json`
  pins each to an exact commit. Commit the lock file so every phase analyzes the
  same source and the numbers are comparable.

## Layout

| Path                     | Purpose                                              |
| ------------------------ | ---------------------------------------------------- |
| `bench/repos.json`       | Manifest: 10 popular repos per language (committed). |
| `bench/repos.lock.json`  | Commit pins for reproducibility (committed).         |
| `bench/.cache/`          | Cloned repositories (gitignored).                    |
| `bench/results/`         | JSON reports, one per run (committable for trends).  |
| `internal/bench`         | Measurement core (unit-tested, no network).          |
| `cmd/sem-bench`          | CLI driver (clone + measure + report).               |

## Usage

```sh
# 1. Pin commits once (writes bench/repos.lock.json) — commit the result.
go run ./cmd/sem-bench -update-lock

# 2. Full run using the pinned commits.
go run ./cmd/sem-bench

# Subset while iterating (fast):
go run ./cmd/sem-bench -languages Go,Rust -limit 3

# Offline: measure repos already in the cache, no cloning.
go run ./cmd/sem-bench -skip-clone

# Print the report to stdout instead of bench/results/.
go run ./cmd/sem-bench -out -
```

Flags: `-manifest`, `-cache`, `-out`, `-lock`, `-languages`, `-limit`, `-jobs`,
`-depth`, `-skip-clone`, `-update-lock`, `-provider-version`.

## Report

Each run writes `bench/results/result-<unix>.json` containing, per repository and
aggregated per language and overall:

- **Performance:** wall time, files, lines of code, files/sec, LOC/sec, output
  bytes, Go allocation bytes.
- **Quality:** symbols, relations, relations by type, resolution distribution
  (`exact`/`import_resolved`/`name_only`/…), confidence bands
  (`exact`/`strong`/`heuristic`/`weak`), parse failures by code, and unresolved
  relative imports.

To compare phases, run with the same lock file and diff the `by_language` and
`totals` aggregates between two `result-*.json` files. Quality regressions show
up as shifts in resolution/confidence distributions or rising parse failures;
performance regressions show up as falling LOC/sec or rising wall time.

## Notes

- Clones are shallow (`-depth 1`) but some repositories are large; a full run
  downloads and analyzes a lot of source. Use `-languages`/`-limit` for quick
  iterations.
- `-update-lock` resolves whatever each repo's default branch currently points
  at (or a manifest-pinned `owner/name@ref`) and records the exact SHA.
