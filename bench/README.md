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

## Tiers

| Tier | Manifest               | Repos               | When                                          |
| ---- | ---------------------- | ------------------- | --------------------------------------------- |
| Fast | `bench/repos.fast.json`| 3/language (72)     | Routine per-phase tracking; runs in minutes.  |
| Full | `bench/repos.json`     | 10/language (240)   | Occasional deep runs; includes mega-repos.    |

The fast tier is a curated subset of the full manifest (no mega-repos like
linux/tensorflow/vscode), so its repos share the same `repos.lock.json` pins and
clone cache — no separate lock needed. The full tier is comprehensive but gated
by a handful of giant repositories and can take 1.5-3 hours.

## Layout

| Path                     | Purpose                                                |
| ------------------------ | ------------------------------------------------------ |
| `bench/repos.json`       | Full manifest: 10 popular repos per language.          |
| `bench/repos.fast.json`  | Fast manifest: 3 repos per language (subset of full).  |
| `bench/repos.lock.json`  | Commit pins for reproducibility (covers both tiers).   |
| `bench/.cache/`          | Cloned repositories (gitignored).                      |
| `bench/results/`         | JSON reports, one per run (gitignored, local-only).    |
| `internal/bench`         | Measurement core (unit-tested, no network).            |
| `cmd/sem-bench`          | CLI driver (clone + measure + report).                 |

## Usage

```sh
# Fast tier — routine per-phase run (minutes):
go run ./cmd/sem-bench -manifest bench/repos.fast.json

# Full tier — comprehensive run (slow; includes mega-repos):
go run ./cmd/sem-bench

# Pin commits once (writes bench/repos.lock.json) — commit the result.
go run ./cmd/sem-bench -update-lock

# Subset while iterating:
go run ./cmd/sem-bench -languages Go,Rust -limit 3

# Offline: measure repos already in the cache, no cloning.
go run ./cmd/sem-bench -skip-clone

# Print the report to stdout instead of bench/results/.
go run ./cmd/sem-bench -out -
```

Both tiers share `bench/repos.lock.json`. After changing the manifests, refresh
pins with `-update-lock` (it records a SHA for every repo across both tiers).

Flags: `-manifest`, `-cache`, `-out`, `-lock`, `-languages`, `-limit`, `-jobs`,
`-depth`, `-skip-clone`, `-update-lock`, `-provider-version`,
`-profile full|fast|syntax-only`. The measured path is `StreamSnapshot`.

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
