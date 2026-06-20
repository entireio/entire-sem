# Provider Baseline Report

Date: 2026-06-18
Schema: `1.1`

This report documents what `entire-sem` emits today, per the v2-plan WP1
("Baseline Audit And Golden Harness"). It is the human-readable companion to the
machine-readable golden baselines under
`internal/sem/testdata/fixtures/*.ndjson.golden`, which are the authoritative
record of the current provider contract.

## Golden Harness

`internal/sem/golden_test.go` (`TestProviderGoldenSnapshots`) snapshots each
fixture repo under `internal/sem/testdata/fixtures/<name>/` in worktree mode and
compares the full NDJSON stream against `<name>.ndjson.golden`. Any change to
symbols, relations, externals, or header stats appears as a golden diff in
review.

- Determinism: fixtures are copied into an isolated temp dir named after the
  fixture, so `repo_key` resolves to a stable `local/<name>` and never inherits
  the host repo's git remote. Worktree mode avoids a machine-specific git error
  in the no-HEAD warning, leaving `repo_root` as the only path-dependent field,
  which the harness normalizes to `<repo>`.
- Regenerate after an intentional contract change:

  ```sh
  go test ./internal/sem -run TestProviderGoldenSnapshots -update
  ```

- Add a fixture: drop a directory under `internal/sem/testdata/fixtures/`, list
  its name in `goldenFixtures`, and run with `-update`.

## Current Fixtures

| Fixture             | Language   | Exercises                                              |
| ------------------- | ---------- | ------------------------------------------------------ |
| `go-basic`          | Go         | type + method, function, imports, call, net/http route |
| `python-basic`      | Python     | class + methods, function, import, constructor/method calls |
| `typescript-basic`  | TypeScript | exported functions, import, intra-file call, route literal |
| `java-basic`        | Java       | class + methods, import, intra-class call              |
| `rust-basic`        | Rust       | struct + impl method, functions, use import            |
| `csharp-basic`      | C#         | namespace + class + methods, using import, intra-class call |
| `php-basic`         | PHP        | namespace + class + methods, use import, `$this->` call |
| `typescript-imports`| TypeScript | relative `./util` import resolved to a local file record   |
| `python-imports`    | Python     | relative `.util` import resolved to a local file record    |
| `java-oo`           | Java       | extends/implements, interface-extends hierarchy            |
| `typescript-oo`     | TypeScript | class extends + implements, interface extends              |
| `csharp-oo`         | C#         | `:` base list split via `I`-prefix heuristic               |
| `php-oo`            | PHP        | class extends + implements                                 |
| `python-oo`         | Python     | multiple inheritance via base list                         |
| `rust-oo`           | Rust       | `impl Trait for Type`, supertrait bounds                   |

All seven Priority-1 languages (per WP9) have committed baselines. Boundary,
service, and IaC fixtures now cover Terraform/HCL, Dockerfile, Docker Compose,
Kubernetes YAML, Kustomize, GitHub Actions, protobuf/gRPC, GraphQL, tRPC, and
Python Flask/FastAPI-style decorator routes, FastAPI/Starlette-style
`include_router(prefix=...)` composition, same-block Express router-prefix
composition, same-name imported Express router mounts, and C# ASP.NET
controller route attributes, plus PHP Laravel controller route declarations and
prefix groups, Symfony/PHP route attributes, Ruby on Rails static route declarations and
`resources` expansion for default REST actions, `only:`, and `except:`, plus
NestJS controller/method decorators;
remaining work is deeper framework coverage and larger-corpus proof.

## Relation Coverage Today

The provider emits the relation types advertised by `capabilities --json`.
Confidence bands follow the v2-plan schema section (`0.90-1.00 exact`,
`0.70-0.89 strong`, `0.40-0.69 heuristic`, `<0.40 weak`).

- Structural: `DEFINES`, `CONTAINS` (1.0).
- Imports: `IMPORTS` (0.8; relative imports resolve to local files at 0.95;
  Go module imports resolved through `go.mod` resolve locally at 0.93; JS/TS
  package self-imports, package `exports`, package `imports`, root/scoped
  import maps, and `tsconfig.json` path aliases resolve locally at 0.89-0.92;
  Python project/module imports, configured source roots, and inferred
  namespace roots resolve locally at 0.88-0.90; exact JVM package imports
  and simple Maven/Gradle package-identity JVM imports resolve locally at 0.90;
  Rust crate/Cargo module imports, deterministic `#[path] mod` aliases,
  and straightforward `pub use` re-exports resolve locally at 0.88).
- Calls: `CALLS` — same-file 0.92, imported 0.86, type-inferred receiver
  0.85-0.9, globally-unique name 0.68.
- OO/type: `EXTENDS`, `IMPLEMENTS` (0.9; C# 0.7 heuristic), `OVERRIDES` (0.85),
  `USES_TYPE` (0.75-0.85).
- Boundaries: `HANDLES_ROUTE` (0.7), `HTTP_CALLS` (0.6-0.7), `HANDLES_TOOL`
  (0.85), `EMITS`/`LISTENS_ON` (0.6, `WEAK_PATTERN`).
- Other: `SIMILAR_TO` (MinHash estimate), `TESTS` (0.8),
  `RESOURCE_DEPENDS_ON` (0.78-0.9 across HCL, Dockerfile, Docker Compose,
  Kubernetes, and Kustomize dependency patterns; Kubernetes named
  ConfigMap/Secret/service-account/PVC/RBAC/owner/Ingress/HPA references,
  Gateway API HTTPRoute backend refs and parent Gateway refs, projected
  ConfigMap/Secret volume refs, ConfigMap/Secret key refs, image pull secrets,
  Service selectors, PodDisruptionBudget selectors, NetworkPolicy pod
  selectors, ServiceMonitor selectors, and PodMonitor selectors, including
  CronJob job-template label targets and Argo Rollout-style workload targets,
  resolve to local resource symbols when those manifests are present. KEDA
  ScaledObject name-only scale targets resolve to local Deployment symbols by
  convention. cert-manager issuer refs, External Secrets secret-store refs,
  Argo WorkflowTemplate refs, Tekton Pipeline/Task refs, Flux CD source refs,
  Crossplane ProviderConfig/Composition/resource refs, and Istio VirtualService
  route destinations and gateway refs, plus DestinationRule hosts, resolve to
  local resource symbols when those manifests are present).

`capabilities --json` reports per-language relation support
(`relation_support_by_language`) and pattern-driven relations separately in
`heuristic_relation_types` (`HANDLES_ROUTE`, `HTTP_CALLS`, `EMITS`,
`LISTENS_ON`, `HANDLES_TOOL`, `HANDLES_GRPC`, `HANDLES_GRAPHQL`,
`HANDLES_TRPC`, `CONFIGURES`, `SIMILAR_TO`, `TESTS`).

Field access (`READS_FIELD`/`WRITES_FIELD`/`ACCESSES`) is emitted for accesses
resolved through the receiver's type (see "Field-access relations" below).

Service/configuration/type/flow expansion is now present in the baseline:
`HANDLES_GRPC`, `HANDLES_GRAPHQL`, `HANDLES_TRPC`, Dockerfile/Kubernetes/
Kustomize/HCL/common project-config `CONFIGURES`, positional
`PARAM_TYPE`/`RETURNS_TYPE`, `ASYNC_CALLS`, `DATA_FLOWS`, and bounded
`FILE_CHANGES_WITH` edges. Remaining work is deeper coverage, not absence of
the relation families: richer cross-statement/cross-symbol data-flow,
higher-precision fallback-format semantics, and larger corpus proof runs.

## Known False Positives / Negatives

These are the documented baseline gaps the later work packages address. They are
intentionally captured in the goldens so improvements show up as diffs.

False positives:

- **Route over-firing — fixed.** `HANDLES_ROUTE` previously fired for any
  path-like string literal in any symbol (gin reported 1039 routes). A route is
  now recorded only when its line carries routing context (an HTTP-verb/route
  method call, mapping decorator, Python Flask/FastAPI-style route decorator,
  FastAPI/Starlette-style `include_router(prefix=...)` mount, Java Spring-style
  direct mapping annotation, Django `path(...)`/simple `re_path(...)`
  registration or URLConf `include(...)` mount, Go `net/http`
  `HandleFunc`/`HandlerFunc` registration, or
  Go chi/gin-style router method registration, C# ASP.NET route/HTTP-verb
  attributes, PHP Laravel route declarations and prefix groups, Symfony/PHP
  route attributes, direct
  Fastify/app/server JS route registrations, Ruby on Rails static route
  declarations and `resources` expansion for default REST actions, `only:`,
  and `except:`, NestJS
  controller/method decorators, Next.js route-file boundaries, or same-block
  Express router mount plus route registration).
  Deterministic static computed route expressions such as
  `apiPrefix + "/health"` and template literals with known local route
  constants compose to one route and do not emit suffixes as separate routes.
  Runtime builders remain intentionally skipped. Matching Python
  `requests`/`httpx`, Java `RestTemplate`/HTTP client calls, Go HTTP client
  calls, C# `HttpClient` calls, PHP `Http::` facade calls, and JS/TS
  `fetch`/Axios calls, including deterministic static computed JS/TS client
  paths and Next.js bracket parameter paths, can bridge to local decorated or
  registered handlers through the shared route endpoint. (WP6.)
- **Global-unique name match (Go `go-basic`).** `LoginHandler CALLS CheckToken`
  is emitted at `0.68` purely because the name is unique repo-wide, not because
  the call was resolved through imports/scope. Correct here, but fragile.
  (WP4: scoped, import-aware call resolution.)

Fixed (kept as a note so the goldens explain the change):

- **Container credited as caller** — a class used to be emitted as `CALLS` of
  its own methods because the member definition lines (`def validate(...)`) match
  the call pattern inside the container's block. Direct-child names are now
  excluded from a container's call scan, so each fixture emits exactly one
  correct `CALLS` edge.

False negatives:

- **Receiver method calls — partially resolved.** Receiver calls are now
  resolved by inferring the receiver's type (`resolution: type_inferred`): a
  `this`/`self` receiver resolves to the enclosing type's method (confidence
  0.9), and a local variable resolves through a constructor assignment
  (`x = new T()` / `x = T()` / `x := T{}`, confidence 0.85). Direct
  constructor chains such as `new Widget().label()` resolve to the local method
  at confidence 0.8. Typed parameters in conservative `name: Type`,
  `Type name`, and `name Type` signatures resolve at confidence 0.83.
  Same-file factory calls such as `makeWidget().label()` resolve at confidence
  0.78, and assigned factory receivers such as
  `const widget = makeWidget(); widget.label()` resolve at confidence 0.77,
  when the factory symbol has an explicit local return type and the target
  method exists on that type. This recovers calls the name-based path drops,
  e.g. Python `service.validate()` and Go `t.Validate()`. Receivers whose type
  can't be inferred still produce no edge — by design, no fabricated targets.
  Remaining: arbitrary returned/chained receivers beyond these high-confidence
  local patterns, plus compiler-grade type flow.
  (WP4.)
- **Imported-symbol calls — external endpoints implemented for common import
  forms.** Go package calls (`strings.TrimSpace`), Python module/member calls
  (`json.dumps` and `from json import dumps`), JS/TS named, default, or
  namespace import calls (`readFileSync`, `path.join`), literal CommonJS
  `require(...)` bindings, and deterministic computed CommonJS bindings now
  emit `CALLS` to
  `external:symbol:<module>.<member>` when no local symbol target resolves.
  Remaining: compiler/type-aware package APIs, arbitrary returned receivers
  beyond explicit same-file factory return types, and arbitrary
  runtime-computed import/module behavior. (WP3/WP4.)
- **Module-root import resolution.** Relative imports (`./util`, `.util`), Go
  module imports covered by `go.mod`, JS/TS package self-imports covered by root
  `package.json` `name`, root `package.json` `exports`/`imports`, root import
  maps, and simple `tsconfig.json` `compilerOptions.paths` aliases, Python
  imports covered by local module files, repo-root `src`, configured setuptools
  package-find roots, inferred nested `*/src` namespace roots, plus
  `pyproject.toml`/`setup.cfg` package names, and exact Java/Kotlin/Scala-style
  package imports covered by package declarations or simple root Maven/Gradle
  package identity now resolve to local file records (`resolution:
  import_resolved`, `target_kind: file`). Remaining
  non-relative local imports that depend on deeper JS/TS conditional
  exports/import-map scopes, complex Python package-dir/editable-install/
  importlib behavior, Maven/Gradle classpath/build-variant behavior beyond
  root package identity, macro-expanded or complex Rust name resolution beyond
  deterministic local module/re-export aliases, or other ecosystem manifests
  remain external.
- **Field-access relations.** `READS_FIELD`/`WRITES_FIELD`/`ACCESSES` are now
  emitted for `receiver.field` accesses resolved through the receiver's type
  (this/self, Go method receiver, constructor-assigned local, or typed
  parameter) to a known `field` symbol. Unresolved/dynamic receivers and bare
  implicit-`this` access are skipped. Remaining: bare-field resolution via
  scope and broader data-flow.
- **Data-flow relations.** `DATA_FLOWS` is emitted when a callee result is
  returned directly (`return helper()`) or assigned to a local and then returned
  as a bare variable (`value = helper(); return value`), including simple
  `if/else` branches where each branch assigns a known callee result to the
  same returned local, and simple conditional return expressions such as
  JS/TS `return flag ? primary() : fallback()` and Python
  `return primary() if flag else fallback()`. Caller parameters passed into
  exact/import-resolved callees, including conservative local alias
  (`alias = input; callee(alias)`), object-field forwarding
  (`payload.field = input; callee(payload)`), simple object-literal forwarding
  (`const payload = { value: input }; callee(payload)`), and
  collection-element forwarding (`values.push(input); callee(values)`) cases,
  also emit lower-confidence caller-to-callee `DATA_FLOWS` edges. Non-returned
  assignments, weak name-only argument calls, returned receiver expressions,
  and arbitrary object/field/collection mutation flow are intentionally
  skipped.
  Remaining: broader object flow, broader branch/control-flow analysis, complex
  aliases, and cross-function data-flow beyond these high-confidence paths.
- **Partial OO/type relations.** `EXTENDS`, `INHERITS`, `IMPLEMENTS`,
  `OVERRIDES`, `USES_TYPE`, `PARAM_TYPE`, and `RETURNS_TYPE` are now emitted.
  `OVERRIDES` only fires when the supertype resolves locally and its methods are
  symbolized, so TypeScript interface methods and Rust trait-impl methods do not
  yet yield overrides. Remaining gaps are deeper parser/type-system coverage,
  not the absence of the relation families.
- **Abstract classes not always captured.** The TypeScript parser does not emit
  a symbol for `export abstract class Base`, so an `EXTENDS` to it falls back to
  an external type endpoint rather than resolving locally. This is a parser
  entity-extraction gap, not a relation bug. (Parser follow-up.)
- **Rust calls are not captured (`rust-basic`).** `token.validate()`, the
  `Token { .. }` literal, and `HashMap::new()` produce no `CALLS` edges, so the
  fixture emits zero calls today. Java/C#/PHP do capture intra-class calls, but
  via the same name-based heuristic (the class is sometimes credited as the
  caller, as in Python). (WP4: per-language call extraction.)

## Header Stats

Every snapshot header carries `stats` with `files`, `parsed_files`, `symbols`,
`relations`, `partial_failures`, and a `completeness_level` of `ok`, `degraded`,
or `unsafe`.

## Schema 1.1 Additive Fields (emitted)

The following optional schema `1.1` fields are now emitted (additive, backward
compatible; tolerant readers ignore unknown fields):

- Header `schema_features`: stable list of optional features present in the
  stream.
- Header `language_versions`: parser/grammar library versions.
- Header `completeness`: parse/index coverage by language (file + symbol counts)
  and by relation type (counts). Per-language `failed_files` is not yet broken
  out.
- Header `benchmark_profile`: emitted only by benchmark tooling (WP10); omitted
  otherwise.
- Relation `relation_scope` (`file`/`module`/`workspace`/`external`),
  `resolution` (`exact`/`import_resolved`/`type_inferred`/`name_only`/
  `pattern`), `target_kind` (`symbol`/`file`/`external`/`route`/`resource`/
  `channel`/`config`), and `evidence` (compact source pointers). Resolution
  values still reserved for consumers or future producers include
  `runtime_trace` and `unresolved`.
