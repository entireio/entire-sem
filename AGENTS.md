# Agent Instructions

This repository is **Entire Graph** (`entire-graph`), an Entire CLI plugin that
provides entity-level checkpoint context and a local-only semantic provider.
The binary is invoked through Entire as `entire graph <command>`.

## Naming

- Product name: **Entire Graph**. Binary and module name: `entire-graph`
  (module `github.com/entireio/entire-graph`).
- The plugin manifest (`entire-plugin.yml`) registers the subcommand name
  `graph`, so users type `entire graph ...`.
- The project was previously named `entire-sem`; do not reintroduce the old
  name. References to **Entire Brain** (`entire-brain`) are intentional — that
  is the separate downstream consumer that owns persistence, indexing, and
  query, not an old name for this project.

## Build, Test, Lint

Tasks are defined in `mise.toml`:

```sh
mise run build     # go build -o entire-graph ./cmd/entire-graph
mise run test      # go test ./...
mise run test:ci   # go test -race ./...
mise run fmt       # gofmt -s -w .
mise run vet       # go vet ./...
mise run check     # fmt + vet + race tests + build
```

CI (`.github/workflows/test.yml`) runs vet, gofmt checks, build, and tests on
Linux, macOS, and Windows with Go 1.24. Building requires CGO because
tree-sitter grammars are native.

## Using the plugin from an agent session

- `entire graph search --repo . --query "..."` — ranked source regions with
  bounded snippets (16 KiB serialized context budget by default). Reads the
  working tree by default so dirty edits are visible; `--head` switches to
  committed-tree semantics.
- `entire graph commit HEAD --json` / `entire graph diff --base A --head B --json` —
  entity-level semantic diff for judging whether a change is safe to keep.
- `entire graph checkpoint <id> --json` — resolves the commit carrying an
  `Entire-Checkpoint: <id>` trailer and diffs it against its first parent.
- `entire graph snapshot|symbols|edges --repo . --format ndjson` — streamed
  machine-readable graph records (schema 1.x, frozen GA contract; see
  `docs/adr/0001-ga-schema-contract.md`).
- `entire graph capabilities --json` / `entire graph doctor --json` — feature
  detection and environment verification. All commands are local-only:
  no network egress, no hosted model calls, no telemetry.

This repository also configures agent harnesses directly: `.claude/settings.json`
and `.codex/` wire Entire session hooks into Claude Code and Codex, and both
define an `entire-search` subagent for checkpoint history search via
`entire search --json`.

## Repository layout

- `cmd/entire-graph/` — thin main; all logic lives in `internal/`.
- `internal/cli/` — hand-rolled command dispatch and flag parsing (no CLI
  framework by design). Keep new commands consistent with this style.
- `internal/sem/` — tree-sitter parsing, provider snapshots, semantic diff,
  and hybrid search. This is the only package that should know about grammars.
- `internal/gitutil/` — git subprocess wrappers (no go-git; NUL-delimited
  output parsing throughout).
- `internal/bench/`, `cmd/graph-bench/`, `bench/` — pinned-corpus benchmark
  harness with throughput and RSS guardrails.
- `docs/` — provider requirements, v2 roadmap, language support matrix,
  benchmarks, and the GA schema ADR.

## Contract rules that agents must not break

- Schema `1.x` is a frozen GA contract: minor versions are additive-only;
  breaking wire-format changes require a major bump (`docs/adr/0001-ga-schema-contract.md`).
- The provider is no-egress: never add code that fetches remote sources,
  calls hosted APIs, uploads telemetry, or downloads grammars at runtime.
- `compound-v1` symbol IDs must remain stable across ordinary body and
  signature edits.
- Unsupported or unparseable files must surface as machine-readable partial
  failures, never silent drops.
- Inventory-only languages must not be presented as semantically parsed
  (`docs/language-support.md`).
