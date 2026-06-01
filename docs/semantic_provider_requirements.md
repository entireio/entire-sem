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
entire sem diff --base main --head HEAD --json
```

Whole-repo outputs should support newline-delimited JSON. Large repositories can
produce hundreds of megabytes of semantic facts, so a single whole-repo JSON
document should be treated as a debug/compatibility mode rather than the primary
integration format.

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
{"record_type":"file","path":"internal/auth/token.go","blob":"..."}
{"record_type":"symbol","id":"...","kind":"function","name":"ValidateToken"}
{"record_type":"relation","from_id":"...","to_id":"...","type":"CALLS"}
```

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

This is stable across content edits but breaks across file moves and some
renames. `entire-sem` should document that breakage and emit enough diff data
for later rename reconciliation using body hash, signature similarity, and
semantic diff records.

If a change report spans a file rename or move that cannot be reconciled to
stable symbols, `entire-sem` should emit an explicit warning instead of silently
dropping edges.

## Relations

Relations should include:

- `from_id`
- `to_id`
- `type`
- `confidence`
- `reason`
- `warning_codes`

Initial relation vocabulary:

- `DEFINES`
- `CONTAINS`
- `IMPORTS`
- `CALLS`
- `IMPLEMENTS`
- `EXTENDS`
- `OVERRIDES`
- `ACCESSES`
- `HANDLES_ROUTE`
- `HANDLES_TOOL`

Relation extraction should grow beyond heuristic dependent counts. Phase 1
should prioritize containment, definitions, imports, calls, and enough route/tool
handler extraction to support local impact analysis.

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
- Go, Python, JavaScript, TypeScript, and Rust support.
- Entity signature and body-hash comparison.
- Checkpoint-aware semantic diffs.

Current gaps:

- No whole-repo graph snapshot.
- No persisted semantic output contract for snapshots.
- Limited relation extraction.
- No stable call/import/type graph contract.
- No stable cross-run symbol identity contract.
- No machine-readable provider capability command.
- No stable warning code vocabulary.
