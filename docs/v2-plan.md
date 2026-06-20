# Detailed Plan: `entire-sem`

Date: 2026-06-18

## Purpose

`entire-sem` should become the reliable semantic-fact provider for Entire's
code-intelligence stack. It should not become a database, MCP server, graph UI,
or agent-memory product. Its job is to parse source locally and emit accurate,
versioned, confidence-scored facts that `entire-brain` can persist and query.

## Current Strengths

- Tree-sitter-backed parser boundary is isolated behind `internal/sem`.
- Supports core languages: Bash, C/C++, C#, CUE, Elixir, Go, Groovy,
  HCL/Terraform, Java, JavaScript/TypeScript, Kotlin, Lua, OCaml, PHP,
  Protocol Buffers, Python, Ruby, Rust, Scala, SQL, Swift, YAML/GitHub Actions,
  plus fallback extraction for Dockerfile, Kustomize, JSON/JSON5, TOML, XML,
  Make, Markdown, HTML/CSS, Vue, and Svelte.
- Emits semantic diffs for commits, checkpoints, and arbitrary refs.
- Emits provider records via `snapshot`, `symbols`, and `edges`.
- Emits `doctor`, `version`, and `capabilities`.
- Has stable `compound-v1` symbol IDs for ordinary edits.
- Reports partial failures and no-egress posture.

## Delivered Capabilities And Remaining Goals

- Relation extraction remains intentionally confidence-scored and honest about
  heuristics.
- Calls are resolved exactly where the provider has enough local information,
  with name-only and pattern-based edges labeled as such.
- Stable symbol IDs include signature disambiguation for duplicate names.
- OO/type relations are emitted: `EXTENDS`, `INHERITS`, `IMPLEMENTS`,
  `OVERRIDES`, `USES_TYPE`, `PARAM_TYPE`, and `RETURNS_TYPE`.
- Field access and high-confidence data-flow relations are emitted:
  `READS_FIELD`, `WRITES_FIELD`, `ACCESSES`, and `DATA_FLOWS`, including direct
  `return helper()`, local assignment followed by bare `return value`, simple
  destructured assignment followed by bare `return value`, simple
  property return from an assigned callee result such as
  `const result = helper(); return result.data`, simple
  branch assignment to the same returned local, simple JS/TS and Python
  conditional return expressions, simple JS/TS and Python fallback return
  expressions, simple JS/TS and Python conditional/fallback assignment followed
  by bare `return value`, exact argument forwarding, conservative
  parameter-property forwarding for direct `param.field` or literal-key
  `param[...]` arguments, conservative parameter-property alias forwarding
  for locals assigned from `param.field` or literal-key `param[...]`,
  conservative parameter-alias forwarding including straight-line multi-hop
  alias chains,
  conservative destructured parameter-alias forwarding, and
  conservative object-field forwarding when a caller parameter is assigned into
  a local object field directly or through a direct alias, simple
  object-literal forwarding including JS/TS `key: param`, JS/TS shorthand
  fields, Python dict literal fields, and direct parameter aliases, plus
  conservative collection-element forwarding when a caller parameter or direct
  alias is inserted into a local collection or array/list literal, and direct
  object or array/list literal argument forwarding to a resolved callee.
- Service and async boundaries are emitted: route/client/channel edges plus
  `HANDLES_GRPC`, `HANDLES_GRAPHQL`, `HANDLES_TRPC`, and `ASYNC_CALLS`.
- IaC/configuration extraction emits HCL dependencies and `CONFIGURES` edges for
  HCL blocks, Dockerfile stages, Kubernetes-looking YAML, Kubernetes
  image/env/port declarations, Kustomize, common JSON/TOML/XML project config,
  Make targets, and GitHub Actions.
- Recent-history co-change extraction emits bounded `FILE_CHANGES_WITH` edges.
- Language coverage is reported exactly through capabilities, warnings, and
  partial failures. Additional formats remain an expansion goal.
- Performance and memory claims are backed by local benchmark tooling; external
  large-corpus proof runs remain an operations task.

## Design Rules

- Stay provider-only: no persistent store, MCP server, graph query language, or
  agent UX in this repo.
- Keep deterministic local indexing no-egress.
- Version every schema change.
- Emit confidence and reasons for every non-trivial relation.
- Prefer partial, honest facts over silent omission.
- Add language features behind fixtures and golden tests.
- Maintain compatibility for Brain: schema `1.0` consumers must fail safely or
  ignore unknown supported-major fields.

## Schema Plan

### Schema `1.1`

Add optional fields while preserving `1.x` compatibility:

- `relation_scope`: `file`, `package`, `module`, `workspace`, `external`.
- `resolution`: `exact`, `import_resolved`, `type_inferred`, `name_only`,
  `pattern`, `runtime_trace`, `unresolved`.
- `confidence`: continue as numeric, but define band meanings:
  `0.90-1.00 exact`, `0.70-0.89 strong`, `0.40-0.69 heuristic`,
  `<0.40 weak`.
- `evidence`: array of compact evidence objects:
  `kind`, `file_path`, `start_line`, `end_line`, `detail`.
- `target_kind`: `symbol`, `file`, `external`, `route`, `resource`, `channel`.
- `warning_codes`: keep stable and machine-readable.

Add header fields:

- `schema_features`: list of optional features emitted.
- `language_versions`: parser/library versions where known.
- `completeness`: aggregate parse/index stats by language and relation type.
- `benchmark_profile`: optional local profile label when emitted by test tools.

### Relation Vocabulary

Keep existing:

- `DEFINES`
- `CONTAINS`
- `IMPORTS`
- `CALLS`
- `HANDLES_ROUTE`
- `HANDLES_TOOL`

Add priority relations:

- `IMPLEMENTS`
- `EXTENDS`
- `INHERITS`
- `OVERRIDES`
- `USES_TYPE`
- `ACCESSES`
- `READS_FIELD`
- `WRITES_FIELD`
- `RETURNS_TYPE`
- `PARAM_TYPE`
- `HTTP_CALLS`
- `HANDLES_GRPC`
- `HANDLES_GRAPHQL`
- `HANDLES_TRPC`
- `EMITS`
- `LISTENS_ON`
- `CONFIGURES`
- `RESOURCE_DEPENDS_ON`
- `TESTS`
- `SIMILAR_TO`

Remain out of provider scope or later expansion:

- cross-repo `CROSS_*` edges, unless Brain asks for provider-level support.
- deeper data-flow beyond high-confidence local direct/assigned return-flow,
  destructured assignment-return flow, assigned property-return flow,
  simple branch-assigned return-flow, simple JS/TS and Python conditional
  return-flow, simple JS/TS and Python fallback return-flow, simple expression
  assignment-then-return flow, exact/import-resolved argument-forwarding flow,
  conservative parameter-property forwarding flow, conservative
  parameter-property alias forwarding flow, conservative parameter-alias
  forwarding flow including straight-line multi-hop alias chains, conservative
  destructured parameter-alias forwarding flow, conservative local
  object-field forwarding flow, conservative local
  object-literal forwarding flow including JS/TS shorthand, Python dict
  literals, and direct parameter aliases, conservative local
  collection-element or collection-literal forwarding flow including direct
  parameter aliases, and direct object/array/list literal argument forwarding.
- deeper semantics for fallback formats where only lightweight structure is
  currently emitted.
- more parser grammars when a real repo or benchmark fixture needs them.

## Work Packages

### WP1: Baseline Audit And Golden Harness

Objective: know what the provider emits today and prevent regressions.

Tasks:

- Add fixture repos under `internal/sem/testdata/fixtures/` for Go, Python,
  TypeScript, Java, Rust, C#, PHP, Terraform, Kubernetes, GitHub Actions.
- Add golden NDJSON tests for:
  files, symbols, externals, relations, warnings, partial failures, header
  completeness, and capabilities.
- Add a relation spot-check harness that labels expected edges by relation type.
- Add performance smoke tests for medium fixtures:
  files indexed per second, peak memory if measurable, output bytes, symbol and
  relation counts.
- Add no-egress tests that fail if provider commands attempt network access.
- Add a fixture-report command or test artifact that Brain can consume for the
  cross-repo capability matrix. Delivered locally as the checked
  `internal/sem/testdata/fixtures/quality_coverage.json` coverage artifact,
  verified by `TestProviderGoldenFixtureQualityCoverageReport`.

Acceptance:

- `go test ./...` validates the current provider contract.
- A baseline report exists for all fixture repos.
- A checked fixture coverage artifact records current language, symbol-kind, and
  relation-type coverage for the golden corpus.
- Current known false positives and false negatives are documented.

### WP2: Stable Identity And Reconciliation

Objective: make symbol IDs and diffs stable enough for Brain history and impact.

Tasks:

- Add a move/rename reconciliation pass for semantic diffs using body hash,
  normalized signature similarity, old/new file path similarity, and container
  lineage.
- Emit explicit `RENAMED`, `MOVED`, or `RECONCILED_FROM` metadata in diff JSON.
- Add duplicate disambiguation that is less line-sensitive:
  include ordinal within container plus signature hash where safe.
- Emit warnings when reconciliation is ambiguous rather than choosing silently.
- Document identity guarantees by symbol kind and language.

Acceptance:

- Moving a file without changing bodies preserves or reconciles symbols.
- Renaming a function with same body is reported as rename, not delete/add, when
  confidence is high.
- Ambiguous duplicate cases produce stable warnings.

### WP3: Import And Module Resolution

Objective: improve the graph backbone used by call resolution and impact.

Tasks:

- Build manifest readers for priority ecosystems:
  `requirements.txt`, `Cargo.toml`, `.csproj`, and `composer.json`. `go.mod`
  module import resolution is implemented for local Go packages present in the
  snapshot; root `package.json` package self-imports, `exports`, `imports`, root
  and scoped import maps, nested workspace/package `package.json` names and
  `exports`, and simple `tsconfig.json` `compilerOptions.paths` aliases are
  implemented for local JS/TS files; `pyproject.toml` and
  `setup.cfg` package names, configured setuptools package-find roots, root and
  package-specific `package-dir`/`package_dir` mappings, inferred nested
  `*/src` namespace roots, and simple `from package import module`
  member-module candidates are implemented for local Python module resolution,
  including literal and local-constant `importlib.import_module(...)`,
  direct or aliased `from importlib import import_module` calls,
  `import importlib as name` aliases, and `__import__(...)` runtime imports; exact
  Java/Kotlin/Scala package imports are implemented through package declarations
  and source file names, with simple root Maven/Gradle package identity aliases;
  `.csproj` root namespace and assembly-name aliases resolve unique C#
  namespace imports to local C# source files; Composer PSR-4 autoload prefixes
  resolve unique PHP namespace imports to local PHP source files; Cargo package names,
  deterministic `#[path] mod` aliases, and straightforward
  `pub use` re-exports are implemented for local Rust module files. JS/TS
  literal CommonJS `require("...")`, literal dynamic
  `import("...")`, and deterministic computed module strings built from known
  local string constants or static array joins emit `IMPORTS`; CommonJS
  bindings from those computed modules also participate in imported external
  call resolution. Maven/Gradle
  classpath/build-variant modeling beyond root package identity, C# compiler
  reference/type usage resolution beyond unique namespace-file matches,
  complex Composer autoload/classmap semantics beyond PSR-4, arbitrary runtime-computed module names, and
  macro-expanded or complex Rust name resolution remain open.
- Normalize module/package roots and file-to-module ownership.
- Resolve relative imports for Go, Python, JS/TS, Rust, Java, C#, PHP.
- Emit `IMPORTS` edges to symbols/files when resolved, external endpoints when
  unresolved.
- Add `resolution` and confidence fields to imports.

Acceptance:

- Fixture imports resolve to local files/modules where possible.
- External imports remain explicit external records.
- Literal and deterministic static computed dynamic imports are represented;
  arbitrary runtime-computed module names remain unresolved rather than
  fabricated.
- Import failures are counted in completeness metrics, not hidden.

### WP4: Calls And Method Resolution

Objective: move from broad name matching to scoped, language-aware call edges.

Tasks:

- Add per-language call extractors for:
  Go, Python, TypeScript/JavaScript, Rust, Java, C#, PHP.
- Scope call lookup by file, module, imports, container/class, receiver, and
  package.
- Distinguish exact calls from unresolved name-only calls.
- Emit imported external call edges for common Go, Python, and JS/TS import
  forms when no local target resolves.
- Avoid global ambiguous matches unless confidence is low and warning-coded.
- Add method receiver resolution for Go, Python classes, TS/JS classes, Java,
  C#, Rust impl blocks, PHP classes.
- Resolve direct constructor-chain calls such as `new Widget().label()` when
  the local type and method symbols are known.
- Resolve typed-parameter receiver calls for conservative `name: Type`,
  `Type name`, and `name Type` signatures.
- Resolve same-file factory-returned receiver calls such as
  `makeWidget().label()` and assigned factory receivers such as
  `const widget = makeWidget(); widget.label()` when the factory has an
  explicit local return type and the target method is known on that type.
- Resolve explicit two- and three-hop same-file returned receiver chains such
  as `makeContainer().widget().label()` and
  `makeContainer().section().widget().label()` when every intermediate method
  has an explicit local return type.
- Emit `CALLS` with `resolution`, `confidence`, `reason`, and evidence.

Acceptance:

- No broad global `sleep`/`run`/`handle` false positive patterns in fixtures.
- Exact local calls rank above imported and name-only matches.
- Imported external calls identify `external:symbol:<module>.<member>` targets
  without fabricating local symbols.
- Direct constructor-chain receiver calls resolve to local methods without
  fabricating arbitrary returned receiver flow.
- Typed-parameter receiver calls resolve only when the parameter type and
  target method are known local symbols.
- Factory-returned receiver calls, including assigned factory receivers and
  explicit two- and three-hop returned receiver chains, resolve only when the
  same file contains explicit local return types and the target method is known
  on the resolved type.
- Brain impact can trust high-confidence direct callers/callees.

### WP5: OO And Type Relations

Objective: support architecture and impact questions that depend on inheritance,
interfaces, traits, and types.

Tasks:

- Extract type declarations and type references per priority language.
- Emit `IMPLEMENTS`, `EXTENDS`/`INHERITS`, `OVERRIDES`, `USES_TYPE`.
- Emit parameter and return type relations where syntax makes this cheap:
  `PARAM_TYPE`, `RETURNS_TYPE`.
- Add trait/interface/implementation fixtures:
  Go interfaces, TypeScript interfaces/classes, Java interfaces/classes, C#
  interfaces/classes, Rust traits/impls, PHP interfaces/classes.

Acceptance:

- Interface implementers and subclass relationships are queryable from provider
  output.
- Override edges are emitted only when confidence is high enough.
- Unsupported dynamic type cases are marked as unresolved, not fabricated.

### WP6: Routes, Tools, Services, And Channels

Objective: make boundary and cross-service analysis competitive.

Tasks:

- Improve route handler detection:
  Express/Fastify/Next.js, Django, Go `net/http`/chi/gin, Java Spring, C#
  ASP.NET, PHP Laravel/Symfony, Ruby on Rails, NestJS, Python Tornado. Django `path(...)`
  registrations, simple `re_path(...)` registrations, and URLConf
  `include(...)` mounts, including string module paths and imported URLConf
  aliases, resolve to local handler symbols when patterns are static. Go
  `net/http` `HandleFunc` registrations and
  `HandlerFunc` wrappers resolve to same-file local handler symbols when paths
  are static or local literal constants; common Go chi/gin-style router method
  registrations do the same. Flask/FastAPI-style Python route decorators are
  implemented for direct app/router decorators. Tornado-style route tuples
  resolve static same-file handler classes. Java Spring-style direct
  mapping annotations are implemented for class-level prefix plus method-level
  route composition. C# ASP.NET controller `[Route]` prefixes and HTTP-verb
  attributes compose and bridge matching `HttpClient` calls; C# minimal API
  `MapGet`/`MapPost` style registrations resolve static same-file handler
  method groups. PHP Laravel route
  declarations and prefix groups resolve local controller methods, and
  Symfony/PHP route attributes compose class and method routes. Direct
  Fastify/app/server JS/TS route registrations and imported or
  CommonJS-exported Fastify plugin routes resolve local handler functions.
  Ruby on Rails static
  route declarations and `resources` declarations with default REST actions,
  `only:`, and `except:` resolve local controller actions. Next.js, SvelteKit,
  and Remix route-file boundaries bridge matching JS/TS clients, including
  bracket-parameter and Remix `$param` paths.
  NestJS controller/method decorators compose class prefixes with method routes
  and bridge matching JS/TS HTTP clients.
  Express/Hono-style JS/TS same-block router mounts plus
  default-imported, CommonJS-exported, same-name, aliased named-import, and namespace-member imported router mounts
  compose `app.use("/prefix", router)` or `app.route("/prefix", router)` with
  static `router.get/post/...` registrations, including local literal constants
  in mount prefixes and child router paths. FastAPI/Starlette-style local
  `include_router(router, prefix="/prefix")` mounts compose with static
  `@router.get/post/...` decorators, including locally resolved relative
  imports. Deterministic static computed route expressions such as
  `apiPrefix + "/health"`, template literals with known local route constants,
  `String.raw` template literals with deterministic local holes, static array
  joins, static `path.join(...)`/`path.posix.join(...)` calls, and
  `new URL("/path", base).pathname` constants compose to a single route
  endpoint.
- Add route client detection:
  `fetch`, Axios, Python requests/httpx, Go `http.Client`, Java HTTP clients,
  C# HttpClient. JS/TS clients support deterministic static computed paths
  built from known local route constants, `String.raw` templates, inline static
  array joins, static `path.join(...)`/`path.posix.join(...)` calls, or
  `new URL("/path", base).pathname` constants; arbitrary runtime builders
  remain out of scope.
- Emit `HANDLES_ROUTE` and `HTTP_CALLS` with method, path, confidence, and
  source evidence.
- GraphQL operation literals emit `HANDLES_GRAPHQL` for named-operation
  compatibility endpoints and selected root-field endpoints, including explicit
  anonymous operations such as `query { viewer { id } }`. JS/TS resolver-map
  fields, modular resolver root objects such as `export const Query = { ... }`,
  and GraphQL schema root fields in `type`/`extend type` `Query`, `Mutation`,
  and `Subscription` blocks now emit `HANDLES_GRAPHQL`. Resolver fields cover inline
  function/arrow handlers, subscription resolver objects, and named/member or
  wrapped resolver references such as `user: getUser`,
  `viewer: userResolvers.viewer`, and `user: withAuth(getUser)`. Matching
  schema root fields emit exact local `CALLS` links to matching resolver
  fields. Full schema validation, type checking, and non-root resolver type
  analysis remain out of scope for the current heuristic pass.
- Add gRPC/protobuf service extraction.
- Add tRPC detection for TypeScript.
- Add channel detection for common pub-sub/event APIs:
  Node EventEmitter, Socket.IO, generic publish/subscribe naming patterns.
- Keep weak pattern detections low-confidence with warning codes.

Acceptance:

- Boundary fixtures produce first-class external route/service/channel records.
- Client-to-route matching works within a repo when paths are static or
  deterministic static computed paths, including inline static array joins;
  exact local matches also emit direct `CALLS` from the client symbol to the
  local route handler/boundary symbol.
- Dynamic route/client paths do not create high-confidence false edges.
- Static computed route paths compose when all parts are local string literals
  or known local route constants, including Express router mount prefixes,
  template-literal route constants, static array joins, and child route paths;
  arbitrary runtime builders remain out of scope.

### WP7: IaC And Resource Graph

Objective: cover the infrastructure files agents frequently need for impact.

Delivered:

- Dockerfile fallback extraction emits stages, `CONFIGURES` edges, and exact
  `RESOURCE_DEPENDS_ON` edges for multi-stage `COPY --from=<stage>`
  dependencies.
- Kubernetes-looking YAML emits config sections plus conservative external
  resource dependencies for common `ConfigMap`, `Secret`, service account, and
  persistent-volume-claim references; when the referenced resource manifests are
  present in the same provider snapshot, those named references also resolve to
  exact local `RESOURCE_DEPENDS_ON` symbol edges. The same exact local
  resolution covers RBAC role/subject references, owner references, Ingress
  Service backends, Gateway API route backend refs, parent Gateway refs, and
  Gateway listener certificateRefs, IngressClass refs, StorageClass refs,
  PersistentVolume refs, RuntimeClass refs, PriorityClass refs, HPA scale
  targets, projected ConfigMap/Secret volume refs, ConfigMap/Secret key refs,
  image pull secrets, and Stakater Reloader ConfigMap/Secret reload
  annotations. When the referring manifest declares
  `metadata.namespace`, namespaced resource references also emit
  namespace-qualified external endpoints such as
  `external:config:kubernetes/secret/web/api-secret`, while preserving the
  existing short endpoint.
- Istio VirtualService route destinations and gateway refs, plus
  DestinationRule hosts, resolve to exact local Service/Gateway resource
  symbols when the referenced manifests are present.
- Kubernetes resource symbols emit common container image, environment-variable,
  and port declarations as `CONFIGURES` facts, multi-document Kubernetes YAML
  emits one resource symbol per document, and Service, PodDisruptionBudget,
  NetworkPolicy, ServiceMonitor, and PodMonitor selectors can depend on
  matching target resources by labels with target-kind filters, including
  CronJob targets whose labels live under `spec.jobTemplate.spec.template` and
  Argo Rollout-style workload targets. KEDA ScaledObject name-only
  `scaleTargetRef` entries and VerticalPodAutoscaler `targetRef` entries
  resolve to workload targets by convention or explicit kind.
- Kustomize manifests emit overlay/resource sections plus external
  dependencies for listed resources, patches, and components.
- Docker Compose manifests emit service resources, exact `depends_on`, `links`,
  `extends.service`, and `network_mode: service:<name>` `RESOURCE_DEPENDS_ON`
  edges between services, and common image/env/port `CONFIGURES` facts.
- Koa/@koa-router `router.routes()` mounts, including static `koa-mount`
  prefixes, compose with static router registrations and bridge exact matching
  HTTP clients to local handlers.
- Flask Blueprint `register_blueprint(..., url_prefix=...)` mounts compose
  with Blueprint route decorators, including locally imported and aliased
  Blueprint variables, and bridge exact matching Python HTTP clients to local
  handlers.
- Go router group prefixes such as `api := e.Group("/api")` compose with
  static child route registrations and bridge exact matching Go HTTP clients to
  local handlers, including chained group calls such as
  `app.Group("/api").Get("/users/{id}", handler)`, nested assigned groups such
  as `v1 := api.Group("/v1")`, and chained groups from assigned parent groups
  such as `api.Group("/admin").Get(...)`.
- Terraform/HCL blocks emit resources, modules, variables, outputs, config
  targets, and exact intra-module `RESOURCE_DEPENDS_ON` edges for block
  references.

Open:

- Cross-file Kubernetes resource resolution is implemented for named
  ConfigMap/Secret/service-account/PVC/RBAC/owner/Ingress/HPA/Gateway API
  HTTPRoute backend, parent Gateway, and Gateway listener certificate
  references, IngressClass, StorageClass, and PersistentVolume references,
  PVC dataSource/dataSourceRef references, VolumeSnapshot PVC source
  references, VolumeSnapshotContent snapshot references, metadata Namespace
  references, RuntimeClass references, PriorityClass references,
  VerticalPodAutoscaler targetRefs, Service selector matches,
  PodDisruptionBudget selector and matchExpression matches, NetworkPolicy podSelector and
  matchExpression matches, Prometheus Operator ServiceMonitor selector and
  matchExpression matches, Prometheus Operator ServiceMonitor auth/TLS Secret
  refs, and Prometheus Operator PodMonitor selector, matchExpression, and
  auth/TLS Secret refs when the target resource symbol exists in the same
  provider snapshot. Workload selector matching includes CronJob job-template
  labels and Rollout-style workload labels. Custom-controller coverage also
  includes KEDA authentication refs, cert-manager issuer refs, External
  Secrets secret-store and target Secret refs, Argo
  WorkflowTemplate refs, Argo Rollouts AnalysisTemplate refs, Argo CD
  Application/AppProject refs, Tekton Pipeline/Task refs, ServiceBinding
  service/workload refs, Knative Trigger broker/subscriber refs, Flux CD source, chart, dependsOn, and HelmRelease valuesFrom ConfigMap/Secret refs,
  Crossplane ProviderConfig/Composition/resource/connection Secret refs, Istio
  VirtualService/DestinationRule refs, and Stakater Reloader ConfigMap/Secret
  reload annotations.
  Remaining Kubernetes resource gaps include less common Kubernetes controllers
  beyond these selectors and other custom resource conventions.
- Broad framework-specific IaC/service modeling remains partial.

Acceptance:

- Brain can answer "which service/deployment/config references this route,
  port, image, env var, or module?"
- IaC parse failures are localized and do not fail source indexing.

### WP8: Semantic Similarity And Clone Hints

Objective: provide useful local similarity facts without making `entire-sem` own
vector search.

Tasks:

- Add MinHash/LSH-style near-clone detection over normalized symbol bodies.
- Emit `SIMILAR_TO` only for high-confidence near duplicates.
- Keep embedding/vector semantic search in Brain, not `entire-sem`.

Acceptance:

- Near-copy fixture functions are linked.
- Common boilerplate and tiny functions are suppressed or low-confidence.

### WP9: Language Coverage Expansion

Objective: expand coverage pragmatically rather than chasing a raw language
count.

Priority 1:

- Improve existing Go, Python, TypeScript/JavaScript, Java, Rust, C#, PHP.

Priority 2:

- HTML/CSS, Vue, Svelte, Markdown code fences, JSON/JSON5, TOML, XML, Gradle,
  Make, Dockerfile, Kubernetes YAML, Kustomize.

Priority 3:

- Additional tree-sitter grammars only when there is a real repo need or
  benchmark fixture.

Acceptance:

- Capabilities reports exact language support and relation support per language.
- Unsupported languages remain visible as partial failures or unsupported hints.

### WP10: Performance And Memory

Objective: support credible local claims, and make the production path
memory-bounded rather than accumulating the whole graph.

Delivered:

- Benchmark harness in `cmd/sem-bench` (driver) and `internal/bench`
  (measurement core), cloning pinned popular repos per language and measuring
  the provider with no network egress. See `docs/benchmarks.md`.
- The benchmark measures the production **streaming** path (`StreamSnapshot`),
  not the in-memory accumulator. Streaming emits records as produced and no
  longer holds full relation payloads, their evidence, or file contents; peak
  memory is bounded by symbol/index metadata plus a compact relation dedup set
  (one 64-bit key per unique relation). The dedup set still scales with the
  count of unique relations — the remaining relation-count-scaled component.
- Indexing profiles `full|fast|syntax-only` (`--profile`) select indexing
  depth; the header declares the profile, its limits, the emitted relation set,
  and skipped relation families. Read throughput numbers from `fast`/`syntax-only`
  and semantic-depth numbers from `full`.
- Reports record profile, hardware (OS/arch/CPUs), process peak RSS, and per-repo
  performance and quality metrics; runs are pinned via `bench/repos.lock.json`
  for cross-phase comparison.

Acceptance (met):

- Performance claims in docs cite reproducible `sem-bench` commands and a named
  profile; docs do not claim memory is independent of relation count.
- Large snapshots stay streaming-friendly and bounded as documented above.

Remaining/optional:

- Optimize only after baseline: parser reuse, worker-pool sizing, file-hash
  cache interface, lower-allocation relation builders.

## Proposed Sequencing

### Phase A: Contract And Baseline

1. WP1 baseline harness.
2. Schema `1.1` draft and compatibility tests.
3. Capabilities output includes per-language relation support.
4. Baseline report committed in `entire-plan` or generated artifacts.

### Phase B: Graph Backbone

1. WP2 identity/reconciliation.
2. WP3 imports/modules.
3. WP4 calls/methods.
4. Update Brain ingestion tests against schema `1.1`.

### Phase C: Rich Architecture Edges

1. WP5 OO/type relations.
2. WP6 routes/services/channels.
3. WP7 IaC resources.
4. Add acceptance fixtures per relation type.

### Phase D: Similarity, Breadth, Performance

1. WP8 near-clone hints.
2. WP9 coverage expansion.
3. WP10 benchmark and optimization.

## Brain Integration Contract

`entire-sem` should provide:

- Stable NDJSON snapshot stream.
- Stable diff JSON.
- Schema version and feature flags.
- Exact warning codes.
- Relation confidence and evidence.
- Completeness metrics by file, language, and relation type.
- No-egress doctor signal.

`entire-brain` should own:

- SQLite stores and indexes.
- Graph query language.
- MCP tool names and schemas.
- Cross-repo workspace links.
- Source snippet retrieval limits.
- Agent-facing summaries and briefs.
- Durable facts, history, patterns, review, and evals.

## Testing Standard

Every new relation type needs:

- At least one positive fixture.
- At least one negative fixture.
- Golden NDJSON assertion.
- Capability reporting assertion.
- Completeness or warning behavior when partially unsupported.
- Brain ingestion compatibility test when the relation becomes consumed.

## Documentation Updates

Update `entire-sem` docs as work lands:

- `README.md`: supported languages, commands, current limits.
- `docs/semantic_provider_requirements.md`: schema and relation vocabulary.
- New `docs/relation-confidence.md`: confidence bands and warning codes.
- New `docs/language-support.md`: exact per-language support matrix.
- New `docs/benchmarks.md`: reproducible benchmark commands and results.

## Risks

- Broad parser coverage can create shallow, low-quality output. Mitigate with
  relation confidence and per-language support matrices.
- Type/LSP-like resolution can become unbounded. Mitigate with priority
  languages and syntax-local resolution before full language-server integration.
- Provider schema churn can break Brain. Mitigate with supported-major
  compatibility tests and feature flags.
- Performance regressions can hide behind richer facts. Mitigate with baseline
  benchmarks before each work package.
- Sharing graph artifacts can leak private symbols/routes/config. Keep sharing
  policy in Brain, not `entire-sem`.

## Definition Of Done

`entire-sem` reaches the graph-provider goal when:

- It emits accurate enough symbols and relations for Brain to implement graph
  schema, graph search, call tracing, impact, snippet retrieval, dead-code, and
  architecture tools.
- Relation confidence is explicit and tested.
- Unsupported or failed files are visible.
- No-egress and deterministic behavior are tested.
- Performance and memory claims are benchmark-backed.
- Brain can consume the provider output without source-specific hacks.
