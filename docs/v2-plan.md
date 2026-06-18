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
  Protocol Buffers, Python, Ruby, Rust, Scala, SQL, Swift, YAML/GitHub Actions.
- Emits semantic diffs for commits, checkpoints, and arbitrary refs.
- Emits provider records via `snapshot`, `symbols`, and `edges`.
- Emits `doctor`, `version`, and `capabilities`.
- Has stable `compound-v1` symbol IDs for ordinary edits.
- Reports partial failures and no-egress posture.

## Next Steps and Goals

- Relation extraction is intentionally heuristic.
- Calls are name-based in many cases and lack language-aware resolution.
- Rename/move reconciliation is weak.
- Duplicate same-name symbols can destabilize IDs.
- OO/type relations are missing from the emitted contract.
- Field access and data-flow relations are missing.
- Cross-service route/client/channel relations are shallow.
- IaC resource extraction is incomplete.
- Language coverage should expand by priority tier and be reported exactly. Support at least 150 languages.
- Performance and memory claims need reproducible benchmark evidence.

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

Defer until a later phase:

- `DATA_FLOWS`
- `ASYNC_CALLS`
- `FILE_CHANGES_WITH`
- cross-repo `CROSS_*` edges, unless Brain asks for provider-level support.

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
  cross-repo capability matrix.

Acceptance:

- `go test ./...` validates the current provider contract.
- A baseline report exists for all fixture repos.
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
  `go.mod`, `package.json`, `tsconfig.json`, `pyproject.toml`, `setup.cfg`,
  `requirements.txt`, `Cargo.toml`, `pom.xml`, `build.gradle`, `.csproj`,
  `composer.json`.
- Normalize module/package roots and file-to-module ownership.
- Resolve relative imports for Go, Python, JS/TS, Rust, Java, C#, PHP.
- Emit `IMPORTS` edges to symbols/files when resolved, external endpoints when
  unresolved.
- Add `resolution` and confidence fields to imports.

Acceptance:

- Fixture imports resolve to local files/modules where possible.
- External imports remain explicit external records.
- Import failures are counted in completeness metrics, not hidden.

### WP4: Calls And Method Resolution

Objective: move from broad name matching to scoped, language-aware call edges.

Tasks:

- Add per-language call extractors for:
  Go, Python, TypeScript/JavaScript, Rust, Java, C#, PHP.
- Scope call lookup by file, module, imports, container/class, receiver, and
  package.
- Distinguish exact calls from unresolved name-only calls.
- Avoid global ambiguous matches unless confidence is low and warning-coded.
- Add method receiver resolution for Go, Python classes, TS/JS classes, Java,
  C#, Rust impl blocks, PHP classes.
- Emit `CALLS` with `resolution`, `confidence`, `reason`, and evidence.

Acceptance:

- No broad global `sleep`/`run`/`handle` false positive patterns in fixtures.
- Exact local calls rank above imported and name-only matches.
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
  Express/Fastify/Next.js, Flask/FastAPI/Django, Go `net/http`/chi/gin, Java
  Spring, C# ASP.NET, PHP Laravel/Symfony.
- Add route client detection:
  `fetch`, Axios, Python requests/httpx, Go `http.Client`, Java HTTP clients,
  C# HttpClient.
- Emit `HANDLES_ROUTE` and `HTTP_CALLS` with method, path, confidence, and
  source evidence.
- Add GraphQL operation and resolver detection.
- Add gRPC/protobuf service extraction.
- Add tRPC detection for TypeScript.
- Add channel detection for common pub-sub/event APIs:
  Node EventEmitter, Socket.IO, generic publish/subscribe naming patterns.
- Keep weak pattern detections low-confidence with warning codes.

Acceptance:

- Boundary fixtures produce first-class external route/service/channel records.
- Client-to-route matching works within a repo when paths are static.
- Dynamic route/client paths do not create high-confidence false edges.

### WP7: IaC And Resource Graph

Objective: cover the infrastructure files agents frequently need for impact.

Tasks:

- Add Dockerfile parser/extractor:
  base images, stages, exposed ports, copied entrypoints.
- Add Kubernetes YAML extractor:
  Deployment, Service, Ingress, ConfigMap, Secret references, env vars,
  service selectors.
- Add Kustomize extractor:
  overlays, resources, patches, bases.
- Add Terraform/HCL resource graph:
  resources, modules, variables, outputs, dependencies.
- Emit resource nodes as external or resource records compatible with Brain.
- Emit `CONFIGURES` and `RESOURCE_DEPENDS_ON`.

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

Objective: support credible local claims.

Tasks:

- Add benchmark command or `go test -bench` suite over fixture repos.
- Track:
  wall time, files/sec, LOC/sec, peak RSS if feasible, output bytes, symbol
  count, relation count, parse failures.
- Add large-repo smoke targets:
  Entire repos, a medium TS repo, a medium Python repo, a Go repo, and one
  mixed IaC repo.
- Optimize only after baseline:
  parser reuse, worker pool sizing, file hash cache interface, streaming
  output, lower allocation relation builders.

Acceptance:

- Performance claims in docs cite reproducible commands.
- Large snapshots stay streaming-friendly and bounded by documented limits.

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
