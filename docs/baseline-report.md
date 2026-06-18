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
| `go-basic`          | Go         | type + method, function, imports, call, HTTP route     |
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

All seven Priority-1 languages (per WP9) now have committed baselines.
Boundary/IaC fixtures (Terraform, Kubernetes, GitHub Actions) follow in WP6/WP7.

## Relation Coverage Today

The provider emits six relation types: `DEFINES`, `CONTAINS`, `IMPORTS`,
`CALLS`, `HANDLES_ROUTE`, `HANDLES_TOOL`. Confidence bands follow the v2-plan
schema section (`0.90-1.00 exact`, `0.70-0.89 strong`, `0.40-0.69 heuristic`).

`capabilities --json` reports per-language relation support
(`relation_support_by_language`): DEFINES/CONTAINS/CALLS for every language plus
IMPORTS where a language-specific scanner exists (HCL, SQL, and YAML have none).
HANDLES_ROUTE and HANDLES_TOOL are reported in `heuristic_relation_types`
because they are path/pattern-driven, not per-language.

Observed confidences:

- `DEFINES`, `CONTAINS`: `1.0` (structural, exact).
- `CALLS` same-file: `0.92`. Imported: `0.86`. Globally-unique name match:
  `0.68` (heuristic).
- `IMPORTS`: `0.8`. `HANDLES_ROUTE`: `0.7`. `HANDLES_TOOL`: `0.85`.

## Known False Positives / Negatives

These are the documented baseline gaps the later work packages address. They are
intentionally captured in the goldens so improvements show up as diffs.

False positives:

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

- **Method calls on receivers are not captured.** Receiver calls such as
  Go `t.Validate()` and Python `service.validate(token)` are intentionally
  skipped (a name preceded by `.`/`->` is not treated as a call) because
  resolving them needs the receiver's type. Implicit-receiver calls
  (`Validate(token)` in Java/C#, `$this->validate()` in PHP) do resolve.
  Receiver-type inference is the remaining WP4 method-resolution task. (WP4.)
- **Imported-symbol calls.** Calls into imported modules (`strings.TrimSpace`,
  `json.dumps`, `readFileSync`) produce no `CALLS` edge to an external endpoint.
  (WP3/WP4.)
- **Module-root import resolution.** Relative imports (`./util`, `.util`) now
  resolve to local file records (`resolution: import_resolved`,
  `target_kind: file`). Non-relative local imports that depend on a manifest
  module root (`go.mod` path, `package.json` name, `tsconfig` paths) are not yet
  resolved and remain external. (WP3 follow-up: manifest readers.)
- **Partial OO/type relations.** `EXTENDS`, `IMPLEMENTS`, and `OVERRIDES` are
  now emitted (`EXTENDS`/`IMPLEMENTS` for Java, TypeScript, JavaScript, C#, PHP,
  Python, Rust; `OVERRIDES` for the class-based languages with method symbols).
  `OVERRIDES` only fires when the supertype resolves locally and its methods are
  symbolized — so TypeScript interface methods and Rust trait-impl methods do
  not yet yield overrides. Still missing: `USES_TYPE`, `PARAM_TYPE`,
  `RETURNS_TYPE`. (WP5 follow-up.)
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
  `resolution` (`exact`/`import_resolved`/`name_only`/`pattern`),
  `target_kind` (`symbol`/`external`/`route`), and `evidence` (compact source
  pointers). Resolution values not yet produced: `type_inferred`,
  `runtime_trace`, `unresolved` (await WP4/WP5).
