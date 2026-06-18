# Semantic Provider Requirements

This document describes the requirements for `entire-sem` to serve as the
semantic provider for Entire Brain.

`entire-sem` should remain responsible for parsing and semantic extraction.
Entire Brain should remain responsible for persistence, indexing, query
behavior, freshness policy, and agent presentation.

## Scope

`entire-sem` is an artifact-emitting provider. It parses source and emits
versioned semantic facts. It should not own the brain store, workspace model, or
agent UX.

Required responsibilities:

- Tree-sitter parsing.
- Entity extraction.
- Language-specific symbol models.
- Semantic diffs.
- Import, call, inheritance, field access, route, and tool relation extraction.
- Parser capability reporting.
- Partial failure reporting.
- Stable provider contracts for downstream consumers.

## Phase 1 Constraints

Phase 1 integration is local-only.

During Phase 1 indexing, `entire-sem` must not:

- Fetch remote code.
- Download grammars or parser assets.
- Upload telemetry.
- Call hosted model APIs.
- Call remote embedding providers.
- Perform implicit network discovery.

`doctor --json` should expose enough information for Entire Brain and CI to
assert that the provider can run without network egress.

## Required Commands

Initial command surface:

```sh
entire sem doctor --json
entire sem version --json
entire sem capabilities --json
entire sem snapshot --repo . --format ndjson
entire sem symbols --repo . --format ndjson
entire sem edges --repo . --format ndjson
entire sem snapshot --repo . --format ndjson --worktree --ignore-file .brainignore
entire sem snapshot --repo . --format ndjson --worktree --include-file .seminclude
entire sem diff --base main --head HEAD --json
```

Whole-repo outputs should support newline-delimited JSON. Large repositories can
produce hundreds of megabytes of semantic facts, so a single whole-repo JSON
document should be treated as a debug/compatibility mode rather than the primary
integration format.

Worktree provider snapshots should honor the repository root `.gitignore` before
walking or reading files. Callers may pass repeatable `--ignore-file <path>`
flags for additional gitignore-style exclusions such as `.brainignore`; relative
ignore-file paths resolve against `--repo`, and missing ignore files should fail
closed with a clear error. Callers may also pass repeatable `--include-file
<path>` flags containing gitignore-style inclusion rules. Include files are
applied after `.gitignore` and `--ignore-file`, so they can reopen otherwise
ignored paths.

## Schema Contract

Provider output uses `schema_version` in `major.minor` form.

Compatibility policy:

- Consumers refuse unknown major versions.
- Consumers may ignore unknown fields within a supported major version.
- If `entire-sem` emits a newer supported-major minor version, consumers should
  warn that some facts may have been skipped.
- Unknown relation types should use an extension namespace, such as
  `X-provider-name:RELATION`.

Initial snapshot header:

```json
{
  "schema_version": "1.0",
  "provider": "entire-sem",
  "provider_version": "0.1.0",
  "repo_root": "/path/to/repo",
  "repo_key": "gh/org/repo",
  "commit": "abc123",
  "tree": "tree789",
  "languages": ["Go", "Python"],
  "capabilities": [],
  "warnings": [],
  "partial_failures": []
}
```

Following records should be typed:

```json
{"record_type":"file","id":"gh/org/repo:file:internal/auth/token.go","path":"internal/auth/token.go","blob":"..."}
{"record_type":"external","id":"external:import:net/http","kind":"import","value":"net/http"}
{"record_type":"symbol","id":"...","kind":"function","name":"ValidateToken"}
{"record_type":"relation","from_id":"...","to_id":"...","type":"CALLS"}
```

Relation endpoints may point to file records, symbol records, or external endpoint
records. Consumers should ignore unknown record types within the supported major
schema version, but should not assume every relation target is a symbol.

## Symbols

Symbols should include:

- `id`
- `stable_id_version`
- `kind`
- `name`
- `qualified_name`
- `file_path`
- `start_line`
- `end_line`
- `signature`
- `body_hash`
- `language`
- `container_id`

The first stable symbol ID version should use a documented compound identity:

```text
<repo-key>:<language>:<file-path>:<kind>:<qualified-name>
```

This is stable across ordinary content edits. Duplicate same-name symbols are
disambiguated by signature hash plus a definition ordinal
(`...#sig:<hash>[#<n>]`) rather than source line ranges, so overloads keep
stable IDs across edits that shift line numbers. File moves and some renames are
reconciled in the semantic diff (see below) rather than in the snapshot ID.
`entire-sem` should document that breakage and emit enough diff data for later
rename reconciliation using body hash, signature similarity, and semantic diff
records.

If a change report spans a file rename or move that cannot be reconciled to
stable symbols, `entire-sem` should emit an explicit warning instead of silently
dropping edges.

Semantic diffs reconcile identity continuity and tag it with explicit
`reconciliation` metadata on each entity change:

- `RENAMED`: a same-file rename (delete+add reconciled by body/signature
  similarity at or above 0.92).
- `MOVED`: a symbol moved across files (a removed entity in one file matched to
  an added entity in another with similarity at or above 0.92). The change
  carries `old_path`/`new_path` and is reported on the destination file; if the
  name also changed, `old_name`/`new_name` are set.

When a move has multiple equally similar destinations (within 0.05), the
provider reports the pair as remove/add and emits a `W_MOVE_AMBIGUOUS` warning
in the diff `warnings` array rather than guessing.

## Relations

Relations should include:

- `from_id`
- `to_id`
- `type`
- `confidence`
- `reason`
- `warning_codes`

Schema `1.1` adds optional relation fields (additive; tolerant readers ignore
unknown fields):

- `relation_scope`: `file`, `module`, `workspace`, `external`.
- `resolution`: how the target was resolved, e.g. `exact`, `import_resolved`,
  `name_only`, `pattern` (later: `type_inferred`, `runtime_trace`,
  `unresolved`).
- `target_kind`: `symbol`, `file`, `external`, `route`, `resource`, `channel`.
- `evidence`: array of compact `{kind, file_path, start_line, end_line, detail}`
  source pointers.

The snapshot header also carries optional `schema_features` (features present in
the stream), `language_versions` (parser/grammar versions), and `completeness`
(coverage by language and relation type).

Relation vocabulary:

- `DEFINES`
- `CONTAINS`
- `IMPORTS`
- `CALLS`
- `EXTENDS` — class extends class, interface extends interface, Rust supertrait.
- `IMPLEMENTS` — class implements interface, Rust `impl Trait for Type`.
- `OVERRIDES` — a method that redefines a same-named method on a resolved
  supertype (derived from EXTENDS/IMPLEMENTS; only when both the supertype and
  its methods are known local symbols).
- `HANDLES_ROUTE`
- `HANDLES_TOOL`

`EXTENDS`/`IMPLEMENTS` are extracted from class/interface headers (Java,
TypeScript, JavaScript, C#, PHP, Python) and from Rust impl/supertrait syntax,
resolved to a local type symbol when one exists or an external `type` endpoint
otherwise. C# cannot syntactically separate a base class from interfaces, so it
uses the `I<Upper>` naming heuristic at lower confidence. Per-language support
is reported in `capabilities` under `relation_support_by_language`.

Relation extraction continues to grow. Still to come: `USES_TYPE`,
`PARAM_TYPE`/`RETURNS_TYPE`, and data-flow relations such as `ACCESSES`.

## Warnings And Partial Failures

Warnings and partial failures must be machine-readable. Free-form strings are
allowed as human detail, but every warning needs:

- stable code
- severity
- file path when applicable
- effect on semantic completeness
- optional human detail

One parser failure should not fail a whole-repo snapshot. The provider should
emit partial failures and continue where possible.

Impact-sensitive consumers need parse-failure thresholds, so `entire-sem` should
report enough aggregate stats to classify downstream reports as `ok`,
`degraded`, or `unsafe`.

## Capability Reporting

`entire sem capabilities --json` should report:

- supported file extensions
- supported languages
- parser versions
- supported relation types
- relation support per language (`relation_support_by_language`): the relation
  types extractable for each language. DEFINES, CONTAINS, and CALLS are
  structural for every language; IMPORTS is listed only where a language-specific
  import scanner exists.
- heuristic relation types (`heuristic_relation_types`): relations such as
  `HANDLES_ROUTE` and `HANDLES_TOOL` that are detected by file-path and body
  patterns rather than per-language grammar, so they are not attributed to a
  single language.
- unsupported-but-detected language hints when available
- whether optional local-only features are available
- whether any feature would require network access

## Tests

Required provider-side tests:

- Golden NDJSON tests for files, symbols, relations, warnings, and partial
  failures.
- Per-language parser fixtures.
- Schema compatibility tests.
- Capability output tests.
- Provider-absent and malformed-output tests at the integration boundary.
- Stable symbol ID tests across content edits.
- Move/rename tests that document known ID breakage or continuity.
- Relation extraction tests for the initial relation vocabulary.
- Partial failure tests proving one bad file does not fail the whole snapshot.
- No-egress tests for Phase 1 operation.
- Performance smoke tests for medium repositories.
- Memory budget tests for cold snapshots.

## Current Foundation

Useful existing foundation:

- Go implementation.
- Isolated tree-sitter parser boundary.
- Bash, C, C++, C#, CUE, Elixir, Go, Groovy, HCL/Terraform, Java,
  JavaScript, TypeScript, Kotlin, Lua, OCaml, PHP, Protocol Buffers, Python,
  Ruby, Rust, Scala, SQL, and Swift support.
- Entity signature and body-hash comparison.
- Checkpoint-aware semantic diffs.

Current implemented foundation:

- Whole-repo NDJSON snapshot output with schema headers.
- Machine-readable provider capability and doctor commands.
- Stable `compound-v1` symbol IDs for ordinary body/signature edits.
- File, symbol, external endpoint, and relation records.
- Stable warning and partial-failure records for unsupported files, syntax errors,
  missing `HEAD`, and explicit working-tree snapshots.

Remaining gaps:

- Relation extraction is still intentionally heuristic.
- File moves, renames, and duplicate same-name symbols need stronger ID
  reconciliation.
- `IMPLEMENTS`, `EXTENDS`, `OVERRIDES`, and `ACCESSES` are future relation types,
  not Phase 1 contract records yet.
- Performance and memory budgets need larger benchmark coverage.
