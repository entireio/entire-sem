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
go test ./internal/bench -run TestMeasureRepoEnforcesLiveRSSGuard
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version guard-test -out - -max-rss-bytes 1
```

## Results

- Full repository tests passed.
- Live benchmark RSS guard tests pass: `MeasureRepoWithOptions` cancels the
  measured provider context as soon as process peak RSS exceeds
  `MaxRSSBytes`, and the CLI `-max-rss-bytes` flag is wired into that live
  guard.
- CLI low-ceiling validation fails as intended with
  `memory guardrail failed during measurement` before completing a repo.
- Capability output reports 182 language/filetype labels and 201 deterministic
  suffixes/extensions.
- Go module imports resolve through `go.mod` to local package files with
  `import_resolved` metadata.
- JS/TS package self-imports, root `package.json` `exports`, root
  `package.json` `imports`, root and scoped import-map entries, and simple
  `tsconfig.json` path aliases resolve to local files with `import_resolved`
  metadata.
- JS/TS literal CommonJS `require(...)` and literal dynamic `import(...)`
  calls emit `IMPORTS`; CommonJS bindings also emit imported external `CALLS`
  when called.
- Python project/module imports resolve through local module files, repo-root
  `src`, configured setuptools package-find roots, inferred nested `*/src`
  namespace roots, `pyproject.toml`, and `setup.cfg` with `import_resolved`
  metadata.
- Exact Java/Kotlin/Scala-style package imports resolve through package
  declarations and source file names with `import_resolved` metadata.
- Simple root Maven/Gradle package identity aliases resolve matching JVM imports
  to local type files with `import_resolved` metadata.
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
- C# ASP.NET controller route attributes compose class-level `[Route]`
  prefixes with method-level HTTP verb attributes, emit `HANDLES_ROUTE`, and
  bridge matching `HttpClient` calls to handlers as direct `CALLS`.
- PHP Laravel route declarations resolve local controller methods, Symfony/PHP
  route attributes compose class and method routes, and matching PHP `Http::`
  facade calls bridge to handlers as direct `CALLS`.
- Ruby on Rails static route declarations and explicit `resources ... only:`
  declarations resolve local controller actions, and matching HTTP client calls
  bridge to handlers as direct `CALLS`.
- Go `net/http` `HandleFunc` registrations and `HandlerFunc` wrappers resolve
  static or local-literal-constant paths to same-file local handler symbols and
  bridge matching Go HTTP client calls as direct `CALLS`.
- Go chi/gin-style router method registrations resolve static or
  local-literal-constant paths to same-file local handler symbols and bridge
  matching Go HTTP client calls as direct `CALLS`.
- Django `path(...)` registrations and simple `re_path(...)` registrations
  resolve static patterns to same-file local handler symbols and bridge
  matching Python HTTP client calls as direct `CALLS`.
- Express-style JS/TS router mounts compose same-block
  `app.use("/prefix", router)` prefixes with static `router.get/post/...`
  routes, emit `HANDLES_ROUTE`, and bridge exact matching `fetch`/Axios client
  calls as direct `CALLS`.
- NestJS controller/method decorators compose class prefixes with method-level
  routes, emit `HANDLES_ROUTE`, and bridge exact matching JS/TS HTTP client
  calls as direct `CALLS`.
- Same-name imported Express routers compose cross-file
  `app.use("/prefix", router)` mounts with static `router.get/post/...`
  registrations and bridge exact matching HTTP client calls to the handler
  symbol.
- Same-name imported Hono-style routers compose cross-file
  `app.route("/prefix", router)` mounts with static `router.get/post/...`
  registrations and bridge exact matching HTTP client calls to the handler
  symbol.
- Aliased named-import and namespace-member imported Express routers compose
  cross-file `app.use("/prefix", router)` mounts with static
  `router.get/post/...` registrations and bridge exact matching HTTP client
  calls to the handler symbol.
- Local literal-constant Express router mount prefixes and child router paths
  compose across files and bridge exact matching HTTP client calls to the
  handler symbol.
- Next.js route-file boundaries bridge matching JS/TS HTTP clients, including
  bracket-parameter client paths normalized to the provider's canonical route
  endpoint form.
- Kubernetes resource extraction, including multi-document YAML manifests, and
  Service-selector dependency tests pass (`Service` -> matching workload
  resource by selector labels).
- PodDisruptionBudget and NetworkPolicy selectors emit exact local
  `RESOURCE_DEPENDS_ON` edges to matching workload resources by labels.
- Service, PodDisruptionBudget, and NetworkPolicy selectors match CronJob
  workload labels under `spec.jobTemplate.spec.template.metadata.labels`.
- Service and PodMonitor selectors match Argo Rollout-style workload labels
  under `spec.template.metadata.labels`.
- ServiceMonitor and PodMonitor selectors emit exact local
  `RESOURCE_DEPENDS_ON` edges to target-kind-filtered Service and workload
  resources by labels.
- PodDisruptionBudget, NetworkPolicy, ServiceMonitor, and PodMonitor
  `matchExpressions` selectors emit exact local `RESOURCE_DEPENDS_ON` edges
  when expressions match target resource labels.
- PodDisruptionBudget and NetworkPolicy `matchExpressions` selectors match
  CronJob workload labels under `spec.jobTemplate.spec.template.metadata.labels`.
- Kubernetes named resource references for ConfigMaps, Secrets, service
  accounts, and PVCs emit exact local `RESOURCE_DEPENDS_ON` edges when the
  referenced resource manifests are present in the snapshot.
- Kubernetes RBAC role/subject references, owner references, Ingress Service
  backends, Gateway API HTTPRoute backend refs, Gateway API HTTPRoute parent
  Gateway refs, and HPA scale targets emit exact local `RESOURCE_DEPENDS_ON`
  edges when the referenced resource manifests are present in the snapshot.
- KEDA ScaledObject name-only scale targets emit exact local
  `RESOURCE_DEPENDS_ON` edges to Deployment resources by convention when the
  target manifest is present in the snapshot.
- Kubernetes projected ConfigMap/Secret volume refs and image pull secrets emit
  external and exact local `RESOURCE_DEPENDS_ON` edges when the referenced
  resource manifests are present in the snapshot.
- Kubernetes ConfigMap/Secret key refs emit external and exact local
  `RESOURCE_DEPENDS_ON` edges when the referenced resource manifests are
  present in the snapshot.
- Kubernetes resource config tests pass for common container image,
  environment-variable, and port declarations emitted as `CONFIGURES` facts.
- Docker Compose service extraction emits service resource symbols, exact
  service-to-service `depends_on` edges, and common image/env/port
  `CONFIGURES` facts.
- Static HTTP client calls bridge to exact local route handlers through shared
  route endpoints as direct `CALLS` edges.
- Deterministic static computed JS/TS route expressions, including
  template-literal route constants and chained concatenated constants, compose
  to route endpoints and bridge matching HTTP clients.
- Deterministic static computed JS/TS HTTP client paths built from known local
  route constants emit `HTTP_CALLS` and bridge to local handlers.
- Imported external calls for common Go, Python, and JS/TS import forms emit
  `CALLS` edges to `external:symbol:<module>.<member>` with
  `resolution: import_external`.
- Deterministic computed JS/TS CommonJS and dynamic import module strings built
  from known local string constants emit `IMPORTS`; computed CommonJS bindings
  also emit imported external `CALLS`.
- Direct constructor-chain receiver calls such as `new Widget().label()` emit
  `CALLS` edges to local methods with `resolution: type_inferred`.
- Same-file factory-returned receiver calls such as `makeWidget().label()` emit
  `CALLS` edges to local methods with `resolution: type_inferred` when the
  factory has an explicit local return type.
- Same-file assigned factory receivers such as
  `const widget = makeWidget(); widget.label()` emit `CALLS` edges to local
  methods with `resolution: type_inferred` when the factory has an explicit
  local return type.
- Typed-parameter receiver calls and field accesses emit local method/field
  relations when the parameter type is a known local symbol.
- Assigned return-flow emits `DATA_FLOWS` when a local variable assigned from a
  known callee is returned as a bare variable.
- Simple branch-assigned return-flow emits `DATA_FLOWS` for known callees
  assigned to the same returned local in `if/else` branches, while preserving
  the sequential reassignment guard.
- Simple conditional return-flow emits `DATA_FLOWS` for known callees returned
  through JS/TS expressions like `return flag ? primary() : fallback()`,
  including an awaited branch, and Python expressions like
  `return primary() if flag else fallback()`, without treating unrelated side
  calls as returned data.
- Exact/import-resolved argument forwarding emits caller-to-callee
  `DATA_FLOWS` when a caller parameter is passed into a known callee.
- Conservative parameter-alias forwarding emits caller-to-callee `DATA_FLOWS`
  when a caller parameter is assigned to a local alias and that alias is passed
  to a known callee.
- Conservative object-field forwarding emits caller-to-callee `DATA_FLOWS` when
  a caller parameter is assigned into a local object field and that object is
  passed to a known callee.
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
  - `bench/results/result-1781949246.json`: Go/gin, syntax-only, 28,618 LOC,
    151,162 LOC/s, max RSS 28,753,920 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781949587.json`: Go/gin, syntax-only, 28,618 LOC,
    160,491 LOC/s, max RSS 28,098,560 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781949766.json`: Go/gin, syntax-only, 28,618 LOC,
    131,698 LOC/s, max RSS 29,261,824 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781949898.json`: Go/gin, syntax-only, 28,618 LOC,
    133,673 LOC/s, max RSS 26,411,008 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781950245.json`: Go/gin, syntax-only, 28,618 LOC,
    160,523 LOC/s, max RSS 27,508,736 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781950390.json`: Go/gin, syntax-only, 28,618 LOC,
    108,781 LOC/s, max RSS 28,573,696 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781950573.json`: Go/gin, syntax-only, 28,618 LOC,
    160,829 LOC/s, max RSS 26,886,144 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781950734.json`: Go/gin, syntax-only, 28,618 LOC,
    113,776 LOC/s, max RSS 26,460,160 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781951308.json`: Go/gin, syntax-only, 28,618 LOC,
    132,835 LOC/s, max RSS 26,738,688 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781951621.json`: Go/gin, syntax-only, 28,618 LOC,
    132,020 LOC/s, max RSS 27,099,136 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781951788.json`: Go/gin, syntax-only, 28,618 LOC,
    131,704 LOC/s, max RSS 28,082,176 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781951876.json`: Go/gin, syntax-only, 28,618 LOC,
    131,577 LOC/s, max RSS 28,835,840 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781951990.json`: Go/gin, syntax-only, 28,618 LOC,
    135,361 LOC/s, max RSS 26,066,944 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781952178.json`: Go/gin, syntax-only, 28,618 LOC,
    163,186 LOC/s, max RSS 28,573,696 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781952750.json`: Go/gin, syntax-only, 28,618 LOC,
    143,831 LOC/s, max RSS 27,181,056 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781952932.json`: Go/gin, syntax-only, 28,618 LOC,
    138,131 LOC/s, max RSS 27,181,056 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781953027.json`: Go/gin, syntax-only, 28,618 LOC,
    138,593 LOC/s, max RSS 28,819,456 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781953248.json`: Go/gin, syntax-only, 28,618 LOC,
    152,499 LOC/s, max RSS 26,771,456 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781953552.json`: Go/gin, syntax-only, 28,618 LOC,
    152,866 LOC/s, max RSS 27,983,872 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781953980.json`: Go/gin, syntax-only, 28,618 LOC,
    152,110 LOC/s, max RSS 29,310,976 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781954379.json`: Go/gin, syntax-only, 28,618 LOC,
    160,307 LOC/s, max RSS 27,770,880 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781954571.json`: Go/gin, syntax-only, 28,618 LOC,
    152,410 LOC/s, max RSS 26,116,096 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781955127.json`: Go/gin, syntax-only, 28,618 LOC,
    148,241 LOC/s, max RSS 26,869,760 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781955405.json`: Go/gin, syntax-only, 28,618 LOC,
    131,801 LOC/s, max RSS 26,755,072 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781955778.json`: Go/gin, syntax-only, 28,618 LOC,
    148,719 LOC/s, max RSS 28,573,696 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781956065.json`: Go/gin, syntax-only, 28,618 LOC,
    161,821 LOC/s, max RSS 28,999,680 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781956681.json`: Go/gin, syntax-only, 28,618 LOC,
    147,713 LOC/s, max RSS 30,048,256 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781956848.json`: Go/gin, syntax-only, 28,618 LOC,
    151,282 LOC/s, max RSS 29,016,064 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781957331.json`: Go/gin, syntax-only, 28,618 LOC,
    135,086 LOC/s, max RSS 27,246,592 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781957549.json`: Go/gin, syntax-only, 28,618 LOC,
    138,238 LOC/s, max RSS 27,983,872 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781958095.json`: Go/gin, syntax-only, 28,618 LOC,
    164,424 LOC/s, max RSS 28,344,320 bytes, output 1,938,906 bytes.
  - `bench/results/result-1781958227.json`: Go/gin, syntax-only, 28,618 LOC,
    129,558 LOC/s, max RSS 28,868,608 bytes, output 1,938,906 bytes.

## Remaining Honesty Notes

- The Linux run is syntax-only proof over a cached local checkout, not a full
  relation-depth benchmark.
- The Linux run reports `completeness_level: unsafe` because generated/large C
  sources still produce parse failures or `E_FILE_TOO_LARGE`; this does not
  invalidate the speed/RSS proof, but it is not quality proof for C semantics.
- Public large-corpus speed claims still need broader retained runs across more
  cached or supplied repositories and profiles.
- Attempted cached C/Linux `fast` profile with a 5 GB RSS ceiling exposed a
  real validation gap before this fix: live process inspection showed the run
  still active at roughly 7.3 GB RSS because the old guard only checked memory
  after completion. That run was terminated and is not counted as retained
  performance proof.
