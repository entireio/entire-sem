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
go build -o /tmp/sem-bench ./cmd/sem-bench
/tmp/sem-bench -skip-clone -manifest bench/repos.json -languages C -limit 1 -profile fast -provider-version codex-fast-c-scan -out bench/results -max-rss-bytes 5000000000 -min-loc-per-sec 150000
go run ./cmd/sem-bench -skip-clone -manifest bench/repos.fast.json -languages Go -limit 1 -profile syntax-only -provider-version codex-k8s-crd-refs -out bench/results
go run ./cmd/sem-bench -manifest bench/repos.fast.json -cache bench/.cache -out bench/results -lock bench/repos.lock.json -languages Go -limit 1 -skip-clone -profile syntax-only -provider-version codex-collection-flow -min-loc-per-sec 1
go run ./cmd/sem-bench -manifest bench/repos.fast.json -cache bench/.cache -out bench/results -lock bench/repos.lock.json -languages Go -limit 1 -skip-clone -profile syntax-only -provider-version codex-istio-resource-refs -min-loc-per-sec 1
go run ./cmd/sem-bench -manifest bench/repos.fast.json -cache bench/.cache -out bench/results -lock bench/repos.lock.json -languages Go -limit 1 -skip-clone -profile syntax-only -provider-version codex-object-literal-flow -min-loc-per-sec 1
go test ./internal/sem -run 'TestStaticArrayJoinRouteExpressionComposesAndBridgesHTTPClient|TestComputedRouteExpressionComposesAndBridgesHTTPClient|TestBuildProviderSnapshotEmitsImportedExternalCalls' -count=1
go test ./...
go run ./cmd/sem-bench -manifest bench/repos.fast.json -cache bench/.cache -out bench/results -lock bench/repos.lock.json -languages Go -limit 1 -skip-clone -profile syntax-only -provider-version codex-array-join-routes -min-loc-per-sec 1
go test ./internal/sem -run 'TestStaticArrayJoinRouteExpressionComposesAndBridgesHTTPClient|TestHTTPCallsBridgeToLocalRouteHandler|TestComputedRouteExpressionComposesAndBridgesHTTPClient' -count=1
go test ./...
go run ./cmd/sem-bench -manifest bench/repos.fast.json -cache bench/.cache -out bench/results -lock bench/repos.lock.json -languages Go -limit 1 -skip-clone -profile syntax-only -provider-version codex-array-join-http-calls -min-loc-per-sec 1
go test ./internal/sem -run 'TestGraphQLSchemaFieldEntities|TestBuildProviderSnapshotEmitsGraphQLSchemaBoundaries|TestBuildProviderSnapshotEmitsGraphQLResolverBoundaries|TestTreeSitterParserTypeScriptGraphQLResolverEntities' -count=1
go run ./cmd/entire-sem capabilities --json
go test ./...
go run ./cmd/sem-bench -manifest bench/repos.fast.json -cache bench/.cache -out bench/results -lock bench/repos.lock.json -languages Go -limit 1 -skip-clone -profile syntax-only -provider-version codex-graphql-schema-fields -min-loc-per-sec 1
```

## Results

- Full repository tests passed.
- Live benchmark RSS guard tests pass: `MeasureRepoWithOptions` cancels the
  measured provider context as soon as process peak RSS exceeds
  `MaxRSSBytes`, and the CLI `-max-rss-bytes` flag is wired into that live
  guard.
- CLI low-ceiling validation fails as intended with
  `memory guardrail failed during measurement` before completing a repo.
- Capability output reports 183 language/filetype labels and 202 deterministic
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
  to local type files with `import_resolved` metadata; `.csproj` root
  namespace and assembly-name aliases resolve unique C# namespace imports to
  local source files with `import_resolved` metadata.
- Composer PSR-4 autoload prefixes resolve unique PHP namespace imports to
  local source files with `import_resolved` metadata.
- Rust `crate::`, `self::`, and Cargo package-name imports resolve to local
  module files with `import_resolved` metadata for conventional source layouts,
  deterministic `#[path] mod` aliases, and straightforward `pub use`
  re-exports.
- Python Flask/FastAPI-style route decorators and Flask `add_url_rule`
  registrations emit `HANDLES_ROUTE`, and matching Python `requests`/`httpx`
  calls bridge to handlers as direct `CALLS` through shared route endpoints.
- FastAPI/Starlette-style `include_router(prefix=...)` mounts compose with
  same-file or locally imported `APIRouter` decorators and bridge matching
  Python HTTP client calls to the handler symbol.
- Tornado-style route tuples in `tornado.web.Application([...])` resolve static
  string or regex route patterns to same-file local handler classes, normalize
  named captures such as `(?P<id>...)` to `{id}`, and bridge matching Python
  HTTP client calls to the handler class.
- Static constant-prefix route expressions such as `apiPrefix + "/health"`
  compose to one route endpoint and avoid emitting the suffix literal as a
  standalone route.
- Java Spring-style route annotations compose class-level prefixes with
  method-level routes, including multi-line mapping arrays with multiple route
  alternatives, emit `HANDLES_ROUTE`, and bridge matching `RestTemplate`/HTTP
  client calls to handlers as direct `CALLS`.
- C# ASP.NET controller route attributes compose class-level `[Route]`
  prefixes with method-level HTTP verb attributes, emit `HANDLES_ROUTE`, and
  bridge matching `HttpClient` calls to handlers as direct `CALLS`.
- C# minimal API registrations such as `app.MapGet("/api/users/{id}",
  ApiHandlers.GetUser)` resolve static route strings or local constants to
  same-file handler functions/methods and bridge matching `HttpClient` calls as
  direct `CALLS`; assigned and chained `MapGroup(...)` prefixes compose with
  child `MapGet`/`MapPost` registrations, including nested assigned groups,
  without emitting unmounted child routes.
- PHP Laravel route declarations, `Route::prefix(...)->group(...)` route
  groups, and `Route::controller(...)->prefix(...)->group(...)` controller
  groups resolve local controller methods, Symfony/PHP route attributes compose
  class and method routes, and matching PHP `Http::` facade calls bridge to
  handlers as direct `CALLS`.
- Ruby on Rails static route declarations, `scope` blocks, `namespace` blocks,
  `resources` declarations with default REST actions, `only:`, `except:`, and
  nested `resources` blocks resolve local controller actions, compose
  route/controller prefixes, and matching HTTP client calls bridge to handlers
  as direct `CALLS`.
- Go `net/http` `HandleFunc` registrations and `HandlerFunc` wrappers resolve
  static or local-literal-constant paths to same-file local handler symbols,
  including unique same-file selector handler expressions, and bridge matching
  Go HTTP client calls as direct `CALLS`.
- Go chi/gin-style router method registrations resolve static or
  local-literal-constant paths to same-file local handler symbols, including
  unique same-file selector handler expressions, and bridge matching Go HTTP
  client calls as direct `CALLS`.
- Go router group prefixes such as `api := e.Group("/api")` compose with
  static child route registrations and bridge matching Go HTTP clients as
  direct `CALLS`; chained group calls such as
  `app.Group("/api").Get("/users/{id}", handler)`, nested assigned groups
  such as `v1 := api.Group("/v1")`, and chained groups from assigned parent
  groups such as `api.Group("/admin").Get(...)` are covered too.
- Django `path(...)` registrations, simple `re_path(...)` registrations, and
  `path(..., include("module.urls"))` URLConf mounts resolve static patterns
  to local handler symbols, including `views.handler` imports, and bridge
  matching Python HTTP client calls as direct `CALLS`.
- FastAPI/Starlette `add_api_route(path, handler)` registrations resolve
  static or local-literal-constant paths to same-file handler symbols,
  including unique same-file selector handler expressions, and avoid
  misclassifying the registration function as the route handler.
- Express-style JS/TS router mounts compose same-block
  `app.use("/prefix", router)` prefixes with static `router.get/post/...`
  routes, emit `HANDLES_ROUTE`, and bridge exact matching `fetch`/Axios client
  calls as direct `CALLS`; unique same-file selector handler expressions are
  covered for local and imported router files, and ambiguous selector members
  are skipped.
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
- Imported Fastify plugin functions registered with
  `app.register(plugin, { prefix })` compose static plugin route registrations
  with the register prefix and bridge exact matching HTTP client calls to the
  handler symbol, including unique same-file selector handler expressions.
- Aliased named-import and namespace-member imported Express routers compose
  cross-file `app.use("/prefix", router)` mounts with static
  `router.get/post/...` registrations and bridge exact matching HTTP client
  calls to the handler symbol.
- Local literal-constant Express router mount prefixes and child router paths
  compose across files and bridge exact matching HTTP client calls to the
  handler symbol.
- Next.js, SvelteKit, and Remix route-file boundaries bridge matching JS/TS
  HTTP clients, including bracket-parameter and Remix `$param` client paths
  normalized to the provider's canonical route endpoint form.
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
  backends, Gateway API route backend refs, Gateway API route parent Gateway
  refs, Gateway listener `certificateRefs`, Gateway API policy `targetRef`/
  `targetRefs`, Ingress `ingressClassName` refs, PVC `storageClassName` refs,
  PVC `volumeName` refs, pod `runtimeClassName` refs, pod
  `priorityClassName` refs, and HPA scale targets emit exact local
  `RESOURCE_DEPENDS_ON` edges when the referenced resource manifests are
  present in the snapshot.
- KEDA ScaledObject name-only scale targets emit exact local
  `RESOURCE_DEPENDS_ON` edges to Deployment resources by convention when the
  target manifest is present in the snapshot. KEDA `authenticationRef` blocks
  emit exact local edges to `TriggerAuthentication` by default and to
  `ClusterTriggerAuthentication` when the ref carries that explicit kind.
- cert-manager `issuerRef`, External Secrets `secretStoreRef`, Argo
  `workflowTemplateRef`/`templateRef`, Argo Rollouts `templateName`
  AnalysisTemplate refs, Tekton `pipelineRef`/`taskRef`, and ServiceBinding
  `service`/`workload`, Knative Trigger `broker`/`subscriber.ref`, and
  Knative Subscription `channel`/`subscriber.ref`/`reply.ref`
  and Knative Serving `Route`/`Service` traffic `revisionName`/
  `configurationName`
  custom-controller references emit exact local `RESOURCE_DEPENDS_ON` edges
  when the referenced resource manifests are present in the snapshot.
- Flux CD `sourceRef`, `chartRef`, same-kind `dependsOn`, and HelmRelease
  `valuesFrom` references emit exact local `RESOURCE_DEPENDS_ON` edges for
  `HelmChart` to `HelmRepository`, `HelmRelease` to
  `HelmRepository`/`HelmChart`/`HelmRelease`/`ConfigMap`/`Secret`, and Flux
  `Kustomization` to `GitRepository`/`Kustomization` when the referenced
  manifests are present, including Flux `kustomization.yaml` files that would
  otherwise collide with ordinary Kustomize overlay detection.
- Crossplane `providerConfigRef`, `compositionRef`, and explicit `resourceRef`
  references emit exact local `RESOURCE_DEPENDS_ON` edges to ProviderConfig,
  Composition, and composite resource manifests when the referenced resources
  are present in the snapshot.
- Istio VirtualService route destinations and gateway refs, plus
  DestinationRule hosts, emit exact local `RESOURCE_DEPENDS_ON` edges to
  Service/Gateway resources when the referenced manifests are present in the
  snapshot.
- Kubernetes projected ConfigMap/Secret volume refs and image pull secrets emit
  external and exact local `RESOURCE_DEPENDS_ON` edges when the referenced
  resource manifests are present in the snapshot.
- Kubernetes ConfigMap/Secret key refs emit external and exact local
  `RESOURCE_DEPENDS_ON` edges when the referenced resource manifests are
  present in the snapshot.
- Kubernetes resource config tests pass for common container image,
  environment-variable, and port declarations emitted as `CONFIGURES` facts.
- Docker Compose service extraction emits service resource symbols, exact
  service-to-service `depends_on`, `links`, `extends.service`, and
  `network_mode: service:<name>` edges, and common image/env/port `CONFIGURES`
  facts.
- Static HTTP client calls bridge to exact local route handlers through shared
  route endpoints as direct `CALLS` edges.
- Deterministic static computed JS/TS route expressions, including
  template-literal route constants, chained concatenated constants, and static
  array joins, compose to route endpoints and bridge matching HTTP clients.
- Deterministic static computed JS/TS HTTP client paths built from known local
  route constants or inline static array joins emit `HTTP_CALLS` and bridge to
  local handlers.
- Imported external calls for common Go, Python, and JS/TS import forms emit
  `CALLS` edges to `external:symbol:<module>.<member>` with
  `resolution: import_external`.
- Deterministic computed JS/TS CommonJS and dynamic import module strings built
  from known local string constants or static array joins emit `IMPORTS`;
  computed CommonJS bindings also emit imported external `CALLS`.
- Direct constructor-chain receiver calls such as `new Widget().label()` emit
  `CALLS` edges to local methods with `resolution: type_inferred`.
- Same-file factory-returned receiver calls such as `makeWidget().label()` emit
  `CALLS` edges to local methods with `resolution: type_inferred` when the
  factory has an explicit local return type.
- Explicit two-hop returned receiver calls such as
  `makeContainer().widget().label()` and constructor-return chains such as
  `new Container().widget().label()` emit `CALLS` edges to the final local
  method when each intermediate method has an explicit local return type.
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
- Simple fallback return-flow emits `DATA_FLOWS` for known callees returned
  through JS/TS expressions like `return primary() || fallback()` and Python
  expressions like `return primary() or fallback()`, while avoiding
  unconditional return-flow labeling for the first branch.
- Simple conditional/fallback assignment-then-return flow emits `DATA_FLOWS`
  for known callees assigned through JS/TS expressions like
  `const value = flag ? primary() : fallback()` and
  `const value = primary() ?? fallback()`, and Python expressions like
  `value = primary() or fallback()`, when the local is returned as a bare
  variable. Sequential overwrite guards prevent stale branch callees from being
  reported when a later assignment wins.
- Simple assigned property-return flow emits `DATA_FLOWS` for known callees
  assigned to a local and returned through a property, such as
  `const result = helper(); return result.data`, while ignoring unrelated
  assigned callees whose locals are not returned.
- Exact/import-resolved argument forwarding emits caller-to-callee
  `DATA_FLOWS` when a caller parameter is passed into a known callee.
- Conservative parameter-alias forwarding emits caller-to-callee `DATA_FLOWS`
  when a caller parameter is assigned to a local alias and that alias is passed
  to a known callee.
- Conservative object-field forwarding emits caller-to-callee `DATA_FLOWS` when
  a caller parameter is assigned into a local object field and that object is
  passed to a known callee.
- Conservative object-literal forwarding emits caller-to-callee `DATA_FLOWS`
  when a caller parameter is assigned into a simple local object literal and
  that object is passed to a known callee.
- Conservative collection-element forwarding emits caller-to-callee
  `DATA_FLOWS` when a caller parameter is pushed/appended/added into a local
  collection and that collection is passed to a known callee.
- JS/TS GraphQL resolver maps now emit concrete `graphql_resolver` symbols and
  `HANDLES_GRAPHQL` edges for `Query`, `Mutation`, and `Subscription` fields;
  GraphQL schema files now emit `graphql_schema_field` symbols and
  `HANDLES_GRAPHQL` edges for root `Query`, `Mutation`, and `Subscription`
  fields in `type` and `extend type` blocks, and matching schema root fields
  emit exact local `CALLS` edges to matching resolver-map fields. These
  complement existing GraphQL operation-literal detection and remain heuristic
  boundary/linking facts, not full schema validation, type checking, or
  non-root resolver type analysis.
- Koa/@koa-router `router.routes()` mounts, including static `koa-mount`
  prefixes and `new Router({ prefix })` constructor prefixes, compose with
  static router registrations and bridge exact matching HTTP clients to local
  handlers.
- Flask Blueprint `register_blueprint(..., url_prefix=...)` mounts compose
  with Blueprint route decorators, while Flask `add_url_rule` positional and
  `view_func=` handlers resolve local functions; both bridge exact matching
  Python HTTP clients to local handlers.
- Retained benchmark reports:
  - `bench/results/result-1781937160.json`: Go/gin, syntax-only, 28,618 LOC,
    152,621 LOC/s.
  - `bench/results/result-1781937166.json`: Go/gin, full profile, 28,618 LOC,
    44,070 LOC/s.
  - `bench/results/result-1781978153.json`: Go/gin, full profile, cached
    checkout, 28,618 LOC, 9,529 relations, 1,391 LOC/s, max RSS 34,635,776
    bytes, allocated 600,541,224 bytes, estimated output 4,936,916 bytes,
    `completeness_level: degraded`; retained as current full-profile smoke
    proof, not as a public full-profile speed claim.
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
  - `bench/results/result-1781969539.json`: Go/gin, syntax-only, 28,618 LOC,
    139,805 LOC/s, max RSS 28,852,224 bytes, estimated output 1,902,629
    bytes; run after JS/TS GraphQL resolver-map extraction.
  - `bench/results/result-1781969714.json`: Go/gin, syntax-only, 28,618 LOC,
    149,934 LOC/s, max RSS 26,394,624 bytes, estimated output 1,902,621
    bytes; run after Flux `chartRef` and `dependsOn` resource-reference
    extraction.
  - `bench/results/result-1781976172.json`: Go/gin, syntax-only, 28,618 LOC,
    151,306 LOC/s, max RSS 29,294,592 bytes, estimated output 1,902,630
    bytes; run after Flux HelmRelease `valuesFrom` ConfigMap/Secret
    resource-reference extraction.
  - `bench/results/result-1781976712.json`: Go/gin, syntax-only, 28,618 LOC,
    154,859 LOC/s, max RSS 27,475,968 bytes, estimated output 1,902,631
    bytes; run after `.csproj` C# namespace import resolution.
  - `bench/results/result-1781976914.json`: Go/gin, syntax-only, 28,618 LOC,
    161,958 LOC/s, max RSS 26,722,304 bytes, estimated output 1,902,630
    bytes; run after Composer PSR-4 PHP namespace import resolution.
  - `bench/results/result-1781970127.json`: Go/gin, syntax-only, 28,618 LOC,
    150,315 LOC/s, max RSS 28,803,072 bytes, estimated output 1,902,626
    bytes; run after KEDA `authenticationRef` resource-reference extraction.
  - `bench/results/result-1781970409.json`: Go/gin, syntax-only, 28,618 LOC,
    140,809 LOC/s, max RSS 28,934,144 bytes, estimated output 1,902,629
    bytes; run after static array-join route/import expression extraction.
  - `bench/results/result-1781970688.json`: Go/gin, syntax-only, 28,618 LOC,
    153,827 LOC/s, max RSS 27,148,288 bytes, estimated output 1,902,633
    bytes; run after inline static array-join HTTP client extraction.
  - `bench/results/result-1781971341.json`: Go/gin, syntax-only, 28,618 LOC,
    141,372 LOC/s, max RSS 28,852,224 bytes, estimated output 1,902,633
    bytes; run after GraphQL schema root field boundary extraction.
  - `bench/results/result-1781977107.json`: Go/gin, syntax-only, 28,618 LOC,
    145,365 LOC/s, max RSS 28,491,776 bytes, estimated output 1,902,639
    bytes; run after GraphQL schema root field to resolver-map linking.
  - `bench/results/result-1781977794.json`: Go/gin, syntax-only, 28,618 LOC,
    148,890 LOC/s, max RSS 27,967,488 bytes, estimated output 1,902,634
    bytes; run after non-root GraphQL schema/resolver field extraction and
    exact schema-field-to-resolver-map linking.
  - `bench/results/result-1781971817.json`: Go/gin, syntax-only, 28,618 LOC,
    146,399 LOC/s, max RSS 29,130,752 bytes, estimated output 1,902,634
    bytes; run after Argo Rollouts AnalysisTemplate reference extraction.
  - `bench/results/result-1781977232.json`: Go/gin, syntax-only, 28,618 LOC,
    151,042 LOC/s, max RSS 27,295,744 bytes, estimated output 1,902,629
    bytes; run after ServiceBinding service/workload reference extraction.
  - `bench/results/result-1781977370.json`: Go/gin, syntax-only, 28,618 LOC,
    164,273 LOC/s, max RSS 29,409,280 bytes, estimated output 1,902,630
    bytes; run after Knative Trigger broker/subscriber reference extraction.
  - `bench/results/result-1781977945.json`: Go/gin, syntax-only, 28,618 LOC,
    171,893 LOC/s, max RSS 27,033,600 bytes, estimated output 1,902,637
    bytes; run after Knative Subscription channel/subscriber/reply reference
    extraction.
  - `bench/results/result-1781978414.json`: Go/gin, syntax-only, 28,618 LOC,
    157,840 LOC/s, max RSS 29,589,504 bytes, estimated output 1,902,640
    bytes; run after Knative Serving traffic revision/configuration reference
    extraction.
  - `bench/results/result-1781972047.json`: Go/gin, syntax-only, 28,618 LOC,
    151,466 LOC/s, max RSS 26,624,000 bytes, estimated output 1,902,634
    bytes; run after Gateway API route backend-ref extraction.
  - `bench/results/result-1781972280.json`: Go/gin, syntax-only, 28,618 LOC,
    164,367 LOC/s, max RSS 26,574,848 bytes, estimated output 1,902,636
    bytes; run after Gateway listener certificateRef extraction.
  - `bench/results/result-1781978698.json`: Go/gin, syntax-only, 28,618 LOC,
    162,018 LOC/s, max RSS 28,114,944 bytes, estimated output 1,902,638
    bytes; run after Gateway API policy targetRef/targetRefs extraction.
  - `bench/results/result-1781979074.json`: Go/gin, syntax-only, 28,618 LOC,
    162,516 LOC/s, max RSS 29,835,264 bytes, estimated output 1,902,638
    bytes; run after unique same-file Go route selector handler extraction.
  - `bench/results/result-1781979249.json`: Go/gin, syntax-only, 28,618 LOC,
    171,318 LOC/s, max RSS 28,999,680 bytes, estimated output 1,902,638
    bytes; run after unique same-file JS/TS route selector handler extraction.
  - `bench/results/result-1781979384.json`: Go/gin, syntax-only, 28,618 LOC,
    165,860 LOC/s, max RSS 27,590,656 bytes, estimated output 1,902,632
    bytes; run after FastAPI/Starlette `add_api_route` handler extraction.
  - `bench/results/result-1781979547.json`: Go/gin, syntax-only, 28,618 LOC,
    171,904 LOC/s, max RSS 27,443,200 bytes, estimated output 1,902,634
    bytes; run after C# minimal API `MapGroup` route extraction.
  - `bench/results/result-1781979910.json`: Go/gin, syntax-only, 28,618 LOC,
    171,072 LOC/s, max RSS 28,442,624 bytes, estimated output 1,902,635
    bytes; run after Spring multi-line mapping array extraction.
  - `bench/results/result-1781980081.json`: Go/gin, syntax-only, 28,618 LOC,
    154,710 LOC/s, max RSS 29,130,752 bytes, estimated output 1,902,637
    bytes; run after Laravel controller-group route extraction.
  - `bench/results/result-1781980407.json`: Go/gin, syntax-only, 28,618 LOC,
    162,532 LOC/s, max RSS 26,951,680 bytes, estimated output 1,902,640
    bytes; run after Rails scope/namespace route extraction.
  - `bench/results/result-1781980824.json`: Go/gin, syntax-only, 28,618 LOC,
    163,919 LOC/s, max RSS 29,163,520 bytes, estimated output 1,902,634
    bytes; run after Rails nested resource route extraction.
  - `bench/results/result-1781981275.json`: Go/gin, syntax-only, 28,618 LOC,
    164,885 LOC/s, max RSS 29,573,120 bytes, estimated output 1,902,634
    bytes; run after Koa router constructor-prefix extraction.
  - `bench/results/result-1781981445.json`: Go/gin, syntax-only, 28,618 LOC,
    164,840 LOC/s, max RSS 27,295,744 bytes, estimated output 1,902,630
    bytes; run after Flask `add_url_rule` route extraction.
  - `bench/results/result-1781972446.json`: Go/gin, syntax-only, 28,618 LOC,
    162,982 LOC/s, max RSS 26,804,224 bytes, estimated output 1,902,630
    bytes; run after IngressClass reference extraction.
  - `bench/results/result-1781972533.json`: Go/gin, syntax-only, 28,618 LOC,
    156,331 LOC/s, max RSS 29,442,048 bytes, estimated output 1,902,624
    bytes; run after StorageClass and PersistentVolume reference extraction.
  - `bench/results/result-1781972617.json`: Go/gin, syntax-only, 28,618 LOC,
    148,588 LOC/s, max RSS 26,886,144 bytes, estimated output 1,902,639
    bytes; run after RuntimeClass and PriorityClass reference extraction.
  - `bench/results/result-1781972763.json`: Go/gin, syntax-only, 28,618 LOC,
    152,329 LOC/s, max RSS 27,803,648 bytes, estimated output 1,902,629
    bytes; run after Koa router mount route extraction.
  - `bench/results/result-1781972991.json`: Go/gin, syntax-only, 28,618 LOC,
    162,362 LOC/s, max RSS 27,344,896 bytes, estimated output 1,902,628
    bytes; run after Flask Blueprint route extraction.
  - `bench/results/result-1781974683.json`: Go/gin, syntax-only, 28,618 LOC,
    150,155 LOC/s, max RSS 27,394,048 bytes, estimated output 1,902,631
    bytes; run after Python Tornado route tuple extraction.
  - `bench/results/result-1781975104.json`: Go/gin, syntax-only, 28,618 LOC,
    147,326 LOC/s, max RSS 26,705,920 bytes, estimated output 1,902,635
    bytes; run after C# minimal API route extraction.
  - `bench/results/result-1781975273.json`: Go/gin, syntax-only, 28,618 LOC,
    149,410 LOC/s, max RSS 29,556,736 bytes, estimated output 1,902,635
    bytes; run after SvelteKit route-boundary extraction.
  - `bench/results/result-1781975769.json`: Go/gin, syntax-only, 28,618 LOC,
    161,684 LOC/s, max RSS 27,328,512 bytes, estimated output 1,902,631
    bytes; run after Remix route-boundary extraction.
  - `bench/results/result-1781975899.json`: Go/gin, syntax-only, 28,618 LOC,
    152,866 LOC/s, max RSS 27,459,584 bytes, estimated output 1,902,640
    bytes; run after assigned property-return data-flow extraction.
  - `bench/results/result-1781975430.json`: Go/gin, syntax-only, 28,618 LOC,
    153,893 LOC/s, max RSS 27,672,576 bytes, estimated output 1,902,641
    bytes; run after Docker Compose service reference dependency extraction.
  - `bench/results/result-1781973208.json`: Go/gin, syntax-only, 28,618 LOC,
    151,226 LOC/s, max RSS 27,066,368 bytes, estimated output 1,902,628
    bytes; run after Go router group-prefix extraction.
  - `bench/results/result-1781973759.json`: Go/gin, syntax-only, 28,618 LOC,
    150,100 LOC/s, max RSS 26,673,152 bytes, estimated output 1,902,636
    bytes; run after chained Go router group-prefix extraction.
  - `bench/results/result-1781973947.json`: Go/gin, syntax-only, 28,618 LOC,
    153,530 LOC/s, max RSS 29,442,048 bytes, estimated output 1,902,632
    bytes; run after fallback return-flow extraction.
  - `bench/results/result-1781974226.json`: Go/gin, syntax-only, 28,618 LOC,
    162,059 LOC/s, max RSS 29,130,752 bytes, estimated output 1,902,633
    bytes; run after nested Go router group-prefix extraction.
  - `bench/results/result-1781974459.json`: Go/gin, syntax-only, 28,618 LOC,
    169,397 LOC/s, max RSS 29,507,584 bytes, estimated output 1,902,642
    bytes; run after conditional/fallback expression assigned-return flow
    extraction.
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
  - `bench/results/result-1781965402.json`: C/Linux cached checkout, fast
    profile, 80,300 files, 39,788,715 LOC, 2,102,504 symbols, 2,542,302
    relations, 136,672.59 ms provider wall time, 291,124 LOC/s, max RSS
    3,753,017,344 bytes, estimated output 1,739,142,766 bytes, 125 parse
    failures, `completeness_level: degraded`.
  - `bench/results/result-1781965760.json`: Go/gin, syntax-only, 28,618 LOC,
    136,147 LOC/s, max RSS 27,410,432 bytes, estimated output 1,902,624
    bytes.
  - `bench/results/result-1781966207.json`: Go/gin, syntax-only, 28,618 LOC,
    130,307 LOC/s, max RSS 29,245,440 bytes, estimated output 1,902,626
    bytes.
  - `bench/results/result-1781966413.json`: Go/gin, syntax-only, 28,618 LOC,
    140,816 LOC/s, max RSS 26,542,080 bytes, estimated output 1,902,626
    bytes.
  - `bench/results/result-1781966708.json`: Go/gin, syntax-only, 28,618 LOC,
    126,735 LOC/s, max RSS 28,229,632 bytes, estimated output 1,902,627
    bytes.
  - `bench/results/result-1781966936.json`: Go/gin, syntax-only, 28,618 LOC,
    142,244 LOC/s, max RSS 28,082,176 bytes, estimated output 1,902,631
    bytes.
  - `bench/results/result-1781967564.json`: Go/gin, syntax-only, 28,618 LOC,
    134,592 LOC/s, max RSS 29,081,600 bytes, estimated output 1,902,631
    bytes.
  - `bench/results/result-1781967930.json`: Go/gin, syntax-only, 28,618 LOC,
    132,509 LOC/s, max RSS 27,246,592 bytes, estimated output 1,902,628
    bytes.
  - `bench/results/result-1781968120.json`: Go/gin, syntax-only, 28,618 LOC,
    145,004 LOC/s, max RSS 27,869,184 bytes, estimated output 1,902,627
    bytes.
  - `bench/results/result-1781968541.json`: Go/gin, syntax-only, 28,618 LOC,
    123,615 LOC/s, max RSS 29,360,128 bytes, estimated output 1,902,627
    bytes.
  - `bench/results/result-1781968842.json`: Go/gin, syntax-only, 28,618 LOC,
    142,102 LOC/s, max RSS 26,853,376 bytes, estimated output 1,902,626
    bytes.
  - `bench/results/result-1781969096.json`: Go/gin, syntax-only, 28,618 LOC,
    79,237 LOC/s, max RSS 26,804,224 bytes, estimated output 1,902,626 bytes.
  - `bench/results/result-1781979074.json`: Go/gin, syntax-only, 28,618 LOC,
    162,516 LOC/s, max RSS 29,835,264 bytes, estimated output 1,902,638
    bytes.
  - `bench/results/result-1781979249.json`: Go/gin, syntax-only, 28,618 LOC,
    171,318 LOC/s, max RSS 28,999,680 bytes, estimated output 1,902,638
    bytes.
  - `bench/results/result-1781979384.json`: Go/gin, syntax-only, 28,618 LOC,
    165,860 LOC/s, max RSS 27,590,656 bytes, estimated output 1,902,632
    bytes.
  - `bench/results/result-1781979547.json`: Go/gin, syntax-only, 28,618 LOC,
    171,904 LOC/s, max RSS 27,443,200 bytes, estimated output 1,902,634
    bytes.
  - `bench/results/result-1781979910.json`: Go/gin, syntax-only, 28,618 LOC,
    171,072 LOC/s, max RSS 28,442,624 bytes, estimated output 1,902,635
    bytes.
  - `bench/results/result-1781980081.json`: Go/gin, syntax-only, 28,618 LOC,
    154,710 LOC/s, max RSS 29,130,752 bytes, estimated output 1,902,637
    bytes.
  - `bench/results/result-1781980407.json`: Go/gin, syntax-only, 28,618 LOC,
    162,532 LOC/s, max RSS 26,951,680 bytes, estimated output 1,902,640
    bytes.

## Remaining Honesty Notes

- The retained fast-profile Linux run is proof over a cached local checkout,
  not clone/network time and not a full-profile relation-depth benchmark.
- The retained fast-profile Linux run used estimated output bytes to avoid
  making benchmark accounting dominate provider runtime; the report records
  `output_bytes_estimated: true`.
- The retained fast-profile Linux run reports `completeness_level: degraded`
  because generated/large C sources still produce parse failures or
  `E_FILE_TOO_LARGE`; this does not invalidate the speed/RSS proof, but it is
  not quality proof for full C semantics.
- Public large-corpus speed claims still need broader retained runs across more
  cached or supplied repositories and profiles.
- Current full-profile retained smoke proof is small-corpus only and much
  slower than syntax-only/fast-profile proof; do not generalize the large-C
  fast-profile speed result to full-profile indexing.
- GraphQL support covers operation literals, JS/TS resolver-map fields, schema
  fields for root and non-root object types, exact schema-field-to-resolver-map
  links, and root GraphQL operation boundaries. It is not full GraphQL schema
  validation, type checking, or resolver analysis beyond exact type/field-name
  matching.
- Attempted cached C/Linux `fast` profile with a 5 GB RSS ceiling exposed a
  real validation gap before this fix: live process inspection showed the run
  still active at roughly 7.3 GB RSS because the old guard only checked memory
  after completion. That run was terminated and is not counted as retained
  performance proof; the later retained fast-profile run above is the current
  speed/RSS proof.
