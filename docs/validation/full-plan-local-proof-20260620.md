# Full Plan Local Proof - 2026-06-20

Branch: `codex/full-plan-implementation`

## Commands

```sh
go test ./...
go run ./cmd/entire-sem capabilities --json
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile full -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.json -languages C -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results -max-rss-bytes 5000000000
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
```

## Results

- Full repository tests passed.
- Capability output reports 182 language/filetype labels and 201 deterministic
  suffixes/extensions.
- Go module imports resolve through `go.mod` to local package files with
  `import_resolved` metadata.
- JS/TS package self-imports and simple `tsconfig.json` path aliases resolve to
  local files with `import_resolved` metadata.
- Kubernetes resource extraction and Service-selector dependency tests pass
  (`Service` -> matching workload resource by selector labels).
- Kubernetes resource config tests pass for common container image,
  environment-variable, and port declarations emitted as `CONFIGURES` facts.
- Static HTTP client calls bridge to exact local route handlers through shared
  route endpoints as direct `CALLS` edges.
- Retained benchmark reports:
  - `bench/results/result-1781937160.json`: Go/gin, syntax-only, 28,618 LOC,
    152,621 LOC/s.
  - `bench/results/result-1781937166.json`: Go/gin, full profile, 28,618 LOC,
    44,070 LOC/s.
  - `bench/results/result-1781937376.json`: C/Linux, syntax-only, 39,798,167
    LOC, 205,436 LOC/s, max RSS 1,634,385,920 bytes.
  - `bench/results/result-1781938318.json`: Go/gin, syntax-only, 28,618 LOC,
    124,871 LOC/s, max RSS 27,639,808 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781939161.json`: Go/gin, syntax-only, 28,618 LOC,
    166,888 LOC/s, max RSS 26,918,912 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781939764.json`: Go/gin, syntax-only, 28,618 LOC,
    148,959 LOC/s, max RSS 26,083,328 bytes, output 1,938,906 bytes.

## Remaining Honesty Notes

- The Linux run is syntax-only proof over a cached local checkout, not a full
  relation-depth benchmark.
- The Linux run reports `completeness_level: unsafe` because generated/large C
  sources still produce parse failures or `E_FILE_TOO_LARGE`; this does not
  invalidate the speed/RSS proof, but it is not quality proof for C semantics.
- Public large-corpus speed claims still need broader retained runs across more
  cached or supplied repositories and profiles.
