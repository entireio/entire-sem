# Full Plan Local Proof - 2026-06-20

Branch: `codex/full-plan-implementation`

## Commands

```sh
go test ./...
go run ./cmd/entire-sem capabilities --json
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile full -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.json -languages C -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results -max-rss-bytes 5000000000
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-full-plan -out bench/results
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
- JS/TS package self-imports, root `package.json` `exports`, root
  `package.json` `imports`, root import-map entries, and simple
  `tsconfig.json` path aliases resolve to local files with `import_resolved`
  metadata.
- Python project/module imports resolve through local module files, repo-root
  `src`, configured setuptools package-find roots, inferred nested `*/src`
  namespace roots, `pyproject.toml`, and `setup.cfg` with `import_resolved`
  metadata.
- Exact Java/Kotlin/Scala-style package imports resolve through package
  declarations and source file names with `import_resolved` metadata.
- Rust `crate::`, `self::`, and Cargo package-name imports resolve to local
  module files with `import_resolved` metadata for conventional source layouts,
  deterministic `#[path] mod` aliases, and straightforward `pub use`
  re-exports.
- Python Flask/FastAPI-style route decorators emit `HANDLES_ROUTE`, and matching
  Python `requests`/`httpx` calls bridge to decorated handlers as direct
  `CALLS` through shared route endpoints.
- FastAPI/Starlette-style `include_router(prefix=...)` mounts compose with
  same-file or locally imported `APIRouter` decorators and bridge matching
  Python HTTP client calls to the handler symbol.
- Static constant-prefix route expressions such as `apiPrefix + "/health"`
  compose to one route endpoint and avoid emitting the suffix literal as a
  standalone route.
- Java Spring-style route annotations compose class-level prefixes with
  method-level routes, emit `HANDLES_ROUTE`, and bridge matching
  `RestTemplate`/HTTP client calls to handlers as direct `CALLS`.
- Express-style JS/TS router mounts compose same-block
  `app.use("/prefix", router)` prefixes with static `router.get/post/...`
  routes, emit `HANDLES_ROUTE`, and bridge exact matching `fetch`/Axios client
  calls as direct `CALLS`.
- Same-name imported Express routers compose cross-file
  `app.use("/prefix", router)` mounts with static `router.get/post/...`
  registrations and bridge exact matching HTTP client calls to the handler
  symbol.
- Kubernetes resource extraction, including multi-document YAML manifests, and
  Service-selector dependency tests pass (`Service` -> matching workload
  resource by selector labels).
- PodDisruptionBudget and NetworkPolicy selectors emit exact local
  `RESOURCE_DEPENDS_ON` edges to matching workload resources by labels.
- Kubernetes named resource references for ConfigMaps, Secrets, service
  accounts, and PVCs emit exact local `RESOURCE_DEPENDS_ON` edges when the
  referenced resource manifests are present in the snapshot.
- Kubernetes RBAC role/subject references, owner references, Ingress Service
  backends, and HPA scale targets emit exact local `RESOURCE_DEPENDS_ON` edges
  when the referenced resource manifests are present in the snapshot.
- Kubernetes projected ConfigMap/Secret volume refs and image pull secrets emit
  external and exact local `RESOURCE_DEPENDS_ON` edges when the referenced
  resource manifests are present in the snapshot.
- Kubernetes resource config tests pass for common container image,
  environment-variable, and port declarations emitted as `CONFIGURES` facts.
- Docker Compose service extraction emits service resource symbols, exact
  service-to-service `depends_on` edges, and common image/env/port
  `CONFIGURES` facts.
- Static HTTP client calls bridge to exact local route handlers through shared
  route endpoints as direct `CALLS` edges.
- Imported external calls for common Go, Python, and JS/TS import forms emit
  `CALLS` edges to `external:symbol:<module>.<member>` with
  `resolution: import_external`.
- Direct constructor-chain receiver calls such as `new Widget().label()` emit
  `CALLS` edges to local methods with `resolution: type_inferred`.
- Same-file factory-returned receiver calls such as `makeWidget().label()` emit
  `CALLS` edges to local methods with `resolution: type_inferred` when the
  factory has an explicit local return type.
- Typed-parameter receiver calls and field accesses emit local method/field
  relations when the parameter type is a known local symbol.
- Assigned return-flow emits `DATA_FLOWS` when a local variable assigned from a
  known callee is returned as a bare variable.
- Exact/import-resolved argument forwarding emits caller-to-callee
  `DATA_FLOWS` when a caller parameter is passed into a known callee.
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
  - `bench/results/result-1781940752.json`: Go/gin, syntax-only, 28,618 LOC,
    148,851 LOC/s, max RSS 26,066,944 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781941213.json`: Go/gin, syntax-only, 28,618 LOC,
    166,849 LOC/s, max RSS 27,459,584 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781941732.json`: Go/gin, syntax-only, 28,618 LOC,
    163,578 LOC/s, max RSS 27,983,872 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781944075.json`: Go/gin, syntax-only, 28,618 LOC,
    150,700 LOC/s, max RSS 29,048,832 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781944293.json`: Go/gin, syntax-only, 28,618 LOC,
    133,281 LOC/s, max RSS 28,033,024 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781944479.json`: Go/gin, syntax-only, 28,618 LOC,
    154,533 LOC/s, max RSS 27,115,520 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781944927.json`: Go/gin, syntax-only, 28,618 LOC,
    149,982 LOC/s, max RSS 28,770,304 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781945145.json`: Go/gin, syntax-only, 28,618 LOC,
    146,917 LOC/s, max RSS 26,116,096 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781945315.json`: Go/gin, syntax-only, 28,618 LOC,
    153,111 LOC/s, max RSS 27,869,184 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781947582.json`: Go/gin, syntax-only, 28,618 LOC,
    139,900 LOC/s, max RSS 27,049,984 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781947870.json`: Go/gin, syntax-only, 28,618 LOC,
    132,607 LOC/s, max RSS 27,721,728 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781948192.json`: Go/gin, syntax-only, 28,618 LOC,
    102,244 LOC/s, max RSS 27,197,440 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781948530.json`: Go/gin, syntax-only, 28,618 LOC,
    110,725 LOC/s, max RSS 28,327,936 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781949067.json`: Go/gin, syntax-only, 28,618 LOC,
    159,290 LOC/s, max RSS 27,852,800 bytes, output 1,938,906 bytes.

## Remaining Honesty Notes

- The Linux run is syntax-only proof over a cached local checkout, not a full
  relation-depth benchmark.
- The Linux run reports `completeness_level: unsafe` because generated/large C
  sources still produce parse failures or `E_FILE_TOO_LARGE`; this does not
  invalidate the speed/RSS proof, but it is not quality proof for C semantics.
- Public large-corpus speed claims still need broader retained runs across more
  cached or supplied repositories and profiles.
