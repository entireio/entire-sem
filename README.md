# Entire Graph

`entire-graph` is an Entire CLI plugin that gives Entire sessions entity-level
checkpoint context and a local semantic code graph.

Entire already knows a checkpoint touched `auth.py` or `.github/workflows/ci.yml`.
This plugin answers the next question: which semantic entities changed inside
that file? It also serves ranked source context for a task and streams a
machine-readable graph of symbols and relations, all without network access.

The plugin builds a binary named `entire-graph`, which is invoked through
Entire as:

```sh
entire graph commit HEAD
entire graph checkpoint abc123def456
entire graph diff --base HEAD~1 --head HEAD
entire graph search --repo . --query "where is service config disabled?"
entire graph snapshot --repo . --format ndjson
entire graph capabilities --json
```

## Why entire-graph

**More accurate than the leading code-memory tool.** On a frozen, multi-language board of 21 real repositories scored on fixed-answer semantic tasks (definition lookup, call graph, imports, change-impact), entire-graph scored **265/283 (94%)** versus **191/283 (68%)** for codebase-memory-mcp. It wins on the questions that matter for navigation and impact, not just node counts.

**Deterministic. No LLM, no vectors.** Pure tree-sitter static analysis. The same commit always produces the same graph. No embeddings, no similarity thresholds, no fuzzy recall to second-guess. A relation is either in the graph or it is not.

**100% local, zero egress.** No API keys, no network calls, no telemetry, no grammar downloads at runtime. `entire graph doctor --json` reports `no_egress=true`. Safe to point at private code.

**Scales to millions of relations on a single machine.** Measured builds on real repositories: **41K relations in 4.8s**, **914K in 45s**, **2.5M in 130s**. No indexing service, no cluster.

**Lean on memory where it counts.** Memory is released after the build. On repositories it fully indexes, peak memory stays in the tens of MB even where codebase-memory-mcp uses hundreds: **80 MB vs 599 MB** on nlohmann/json, **64 MB vs 285 MB** on koin, **43 MB vs 225 MB** on Dapper.

**36 languages, one binary.** Tree-sitter grammars compiled in, plus 149 inventory-only filetypes. Nothing to install at runtime, nothing that breaks.

**A 30-relation code graph with stable identity.** `compound-v1` symbol IDs survive ordinary edits, and relations span structure, calls, inheritance, fields, service boundaries, and config dependencies, each tagged with resolution and confidence.

**Fewer tokens for coding agents.** A structural question that costs an agent a file-reading sweep becomes one graph call. Answering "where is it defined, who calls it, what does it call" for a core symbol like `redisCommand` in redis takes about **2,300 tokens** from the graph versus about **654,000 tokens** to read the 23 files that reference it: a **283x reduction** (measured with tiktoken). The more widely a symbol is used, the larger the saving.

## What It Adds to Entire

- **Entity-level checkpoint context.** `commit`, `checkpoint`, and `diff` report
  added, removed, renamed, and signature- or body-changed entities with heuristic
  dependent counts, so a checkpoint can be judged without reading the full diff.
- **A local-only semantic provider.** `snapshot`, `symbols`, and `edges` stream
  NDJSON graph records under a frozen schema 1.x contract for downstream
  consumers such as Entire Brain: no network, no hosted models, no telemetry.
- **Tree-sitter parsing across 36 languages**, from Go and TypeScript to
  Terraform, SQL, and GitHub Actions YAML, with 149 more filetypes tracked as
  inventory-only and honest partial failures for everything else.
- **A 30-relation code graph with stable identity.** `compound-v1` symbol IDs
  survive ordinary edits, and relations cover structure, calls, inheritance,
  fields, service boundaries, and config dependencies, each tagged with
  resolution and confidence.
- **Agent-ready hybrid search.** Lexical scoring fused with graph-neighbor
  expansion returns diverse source regions from the live working tree, budgeted
  to 16 KiB for direct use in a model context window.
- **How it works.** The plugin shells out to git for trees and diffs, parses
  before/after states with tree-sitter behind `internal/sem`, reconciles renames
  and moves by similarity, and streams results so memory stays bounded on very
  large repositories.

## Language Support

Tree-sitter-backed semantic parsing covers 36 languages: Bash, C, C++, C#,
Clojure/ClojureScript, CUE, Dart, Elixir, Erlang, F#, Go, Groovy, Haskell,
HCL/Terraform, Java, JavaScript, Julia, Kotlin, Lua, Objective-C, OCaml, Perl,
PHP, Protocol Buffers, Python, R, Ruby, Rust, Scala, SQL, Swift, TypeScript,
YAML (including GitHub Actions workflow sections and jobs), Zig, and Zsh.

Another 149 filetypes are recognized as inventory-only: stable file records
without semantic claims. See [docs/language-support.md](docs/language-support.md)
for the full two-tier matrix.

The parser is isolated behind `internal/sem`, so the command surface stays
stable while the semantic model gets richer.

## Getting Started

Requirements:

- Entire CLI
- Git
- Go toolchain with CGO support (tree-sitter uses native parser bindings)

1. Install the plugin binary from the default branch and register it with
   Entire's managed plugin directory:

   ```sh
   go install github.com/entireio/entire-graph/cmd/entire-graph@main
   entire plugin install "$(go env GOPATH)/bin/entire-graph" --force
   ```

2. Verify the install and the local-only environment:

   ```sh
   entire graph version
   entire graph doctor --json
   ```

3. Run your first semantic diff from any git repository:

   ```sh
   entire graph commit HEAD
   ```

If `$(go env GOPATH)/bin` is already on your `PATH`, Entire can also discover
the binary directly after `go install`.

Entire plugins are currently local executables, not a hosted plugin marketplace:
`entire graph` works because Entire discovers an `entire-graph` binary from its
managed plugin directory or from `$PATH`.

## Install From Source

```sh
git clone https://github.com/entireio/entire-graph.git
cd entire-graph
mise run build
entire plugin install ./entire-graph --force
```

After either install path, `entire graph ...` works anywhere the Entire CLI can
find the managed plugin.

For a one-command local source install, run `scripts/install-local.sh`. For
local release archives with `SHA256SUMS`, run `scripts/release.sh`. See
[docs/operations.md](docs/operations.md) for target and cgo details.

## Commands

### Semantic Diff

Compare one commit against its first parent, compare two arbitrary refs, or
analyze the commit associated with an Entire checkpoint trailer:

```sh
entire graph commit HEAD
entire graph diff --base main --head HEAD
entire graph diff --base main --head HEAD --json
entire graph checkpoint abc123def456
```

`analyze` is an alias for `diff`. All four commands print human-readable text
by default and structured output with `--json`. Example:

```text
Semantic changes HEAD~1..HEAD

auth.py
  ~ function validate_token signature changed (14 dependents)
  + class TokenClaims added
  - function parse_token removed (0 dependents)
```

### Search

Search the live working tree for ranked source regions:

```sh
entire graph search --repo . --query "where is service config disabled?" --top-k 20
```

`search` combines source-body matching, identifier splitting, symbol names and
signatures, paths, conceptual issue-to-API terms, and diversity-aware region
selection. It can return several non-overlapping regions from one file. Results
include broad source provenance plus a focused, bounded snippet for direct agent
context.

Interactive search reads the working tree by default so agents see dirty edits.
Use `--head` for immutable committed-tree semantics. Cold search first scans
files without parsing, then indexes at most 96 query-relevant files; use
`--max-indexed-files` to change the bound or `--index-all-files` for exhaustive
parsing. The default `syntax-only` profile avoids synchronous whole-repository
graph construction. `--profile fast` adds local relation expansion when deeper
semantic indexing is worth the cost.

When Entire supplies `ENTIRE_PLUGIN_DATA_DIR`, committed-tree searches reuse a
tree-keyed compressed index. Direct callers can set `--cache-dir`; `--no-cache`
disables persistence. Worktree searches do not reuse committed indexes, avoiding
stale results after edits.

### Provider Records

Emit machine-readable graph records:

```sh
entire graph snapshot --repo . --format ndjson
entire graph symbols --repo . --format ndjson
entire graph edges --repo . --format ndjson
entire graph snapshot --repo . --format ndjson --worktree --ignore-file .brainignore
entire graph snapshot --repo . --format ndjson --worktree --include-file .graphinclude
```

`snapshot` writes a complete NDJSON stream: one header record followed by file,
symbol, and relation records. `symbols` and `edges` are filtered views over the
same snapshot builder. By default, snapshots read the committed `HEAD` tree when
git metadata is available, so dirty tracked edits and untracked files are excluded.
Use `--worktree` to include live working-tree contents, and `--no-network` to make
the provider's no-egress contract explicit to callers. Worktree snapshots honor
the repository root `.gitignore`. Pass repeatable `--ignore-file <path>` flags for
additional gitignore-style exclusions, resolved relative to `--repo` unless the
path is absolute. Pass repeatable `--include-file <path>` flags for gitignore-style
inclusion rules that are applied after `.gitignore` and `--ignore-file`, allowing
callers to reopen otherwise ignored paths.

### Environment

Inspect the provider and its environment:

```sh
entire graph version --json
entire graph doctor --json
entire graph capabilities --json
```

### Running Without Entire

```sh
ENTIRE_REPO_ROOT=/path/to/repo ./entire-graph diff --base HEAD~1 --head HEAD
```

## Agent Usage

Entire Graph is built to be called by coding agents, not just humans. Every
command is local-only and no-egress, so agents can run it freely inside
sandboxed sessions without leaking source. Typical agent workflows:

- **Find where to work.** Before editing, retrieve ranked code regions for a
  task description:

  ```sh
  entire graph search --repo . --query "retry logic for webhook delivery" --format json
  ```

  Search reads the working tree by default, so an agent sees its own dirty
  edits. Output is budgeted to 16 KiB of serialized snippets by default,
  sized for direct inclusion in a model context window. Use `--top-k` and
  `--max-context-bytes` to tune the budget.

- **Judge a checkpoint.** After a change, summarize what actually changed at
  the entity level to decide whether to keep, revert, or continue:

  ```sh
  entire graph commit HEAD --json
  entire graph checkpoint <checkpoint-id> --json
  ```

  Signature changes with high dependent counts are a strong signal to run
  tests before proceeding.

- **Build a code graph.** Ingest machine-readable symbols and relations into
  agent memory or a downstream store such as Entire Brain:

  ```sh
  entire graph snapshot --repo . --format ndjson
  ```

  Records carry stable `compound-v1` IDs, so entities can be tracked across
  sessions and ordinary edits.

- **Detect capabilities first.** Agents should feature-detect instead of
  assuming: `entire graph capabilities --json` lists semantic versus
  inventory-only languages and supported relation types, and
  `entire graph doctor --json` verifies the environment, including
  `no_egress=true`.

Inside Entire sessions, the Entire CLI sets `ENTIRE_REPO_ROOT` and
`ENTIRE_PLUGIN_DATA_DIR` automatically; committed-tree searches then reuse a
tree-keyed compressed index across invocations. Outside Entire, pass `--repo`
explicitly. See [AGENTS.md](AGENTS.md) for agent instructions specific to
working on this repository.

## Provider Contract

The provider contract is designed for Entire or other local tools that want a
stable semantic graph rather than human-oriented diff text.

- `version --json` returns the provider name and plugin version.
- `doctor --json` reports Entire environment variables, repository resolution,
  plugin data directory writability, and `no_egress=true`.
- `capabilities --json` reports supported extensions, languages, parser metadata,
  relation types, optional local-only features, and network requirements.
- `snapshot --format ndjson` emits a header plus file, external endpoint,
  symbol, and relation records.
- `symbols --format ndjson` emits the same header followed by symbol records.
- `edges --format ndjson` emits the same header followed by relation records.
- `search --query ...` emits ranked, qrel-blind source regions as JSON, NDJSON,
  or text without network access or a hosted model. Search output is bounded to
  16 KiB of serialized result context by default; use `--max-context-bytes 0`
  for an unbounded diagnostic ranking.

Snapshot headers include the schema version, provider version, repository key,
`HEAD` commit and tree when available, parsed languages, capability labels,
warnings, partial failures, and aggregate completeness stats. File records include
stable file IDs so `DEFINES` relation endpoints can be resolved. Symbol records
include stable compound IDs, `stable_id_version`, kind, qualified name, source
range, signature, body hash, language, and container ID when the symbol is nested.
External endpoint records describe relation targets such as imported modules,
routes, and tool handlers.

Schema 1.1 defines 30 relation types. Structural: `DEFINES`, `CONTAINS`,
`IMPORTS`. Calls and construction: `CALLS`, `CONSTRUCTS`, `ASYNC_CALLS`.
Types and inheritance: `EXTENDS`, `INHERITS`, `IMPLEMENTS`, `OVERRIDES`,
`USES_TYPE`, `PARAM_TYPE`, `RETURNS_TYPE`. Fields: `READS_FIELD`,
`WRITES_FIELD`, `ACCESSES`. Boundaries: `HANDLES_ROUTE`, `HANDLES_GRPC`,
`HANDLES_GRAPHQL`, `HANDLES_TRPC`, `HTTP_CALLS`, `EMITS`, `LISTENS_ON`,
`HANDLES_TOOL`. Configuration and analysis: `CONFIGURES`, `SIMILAR_TO`,
`TESTS`, `RESOURCE_DEPENDS_ON`, `DATA_FLOWS`, `FILE_CHANGES_WITH`.

Indexing profiles emit subsets: `syntax-only` emits `DEFINES` and `CONTAINS`,
`fast` adds imports, shallow calls, and boundary relations, and `full` emits
every relation type with evidence fields. `capabilities --json` reports the
supported relation types per language.

Unsupported but detected source files are reported as machine-readable partial
failures instead of disappearing silently. Supported files with tree-sitter syntax
errors are also reported as partial failures while any recoverable symbols are
still emitted. Repositories without a readable `HEAD` fall back to the working tree
and include a warning in the snapshot header.

`compound-v1` IDs are stable across ordinary body/signature edits because they use
repo key, language, path, kind, and qualified name. Duplicate same-name symbols in
one file are disambiguated with source ranges, so inserted lines above duplicates
can change those duplicate IDs; move/rename reconciliation is left to semantic diff
records.

## Why This Exists

When an agent changes a repo, you often need to decide if the checkpoint is safe
to keep, revert, or continue from. File names are not enough. `entire-graph` shows
the actual code entities, workflow sections, signatures, and renames that changed,
so you can understand the checkpoint before reading the full diff.

Issue [entireio/cli#589](https://github.com/entireio/cli/issues/589) proposes showing
checkpoint context at the entity level instead of stopping at "this file changed."
`entire-graph` is a plugin-shaped implementation of that idea:

- parse the before and after git trees with tree-sitter
- extract named entities like functions, classes, methods, structs, traits, types,
  YAML workflow sections, and GitHub Actions jobs
- compare signatures and normalized bodies
- build a heuristic dependent count from parsed references in the target tree
- report added, removed, renamed, signature-changed, and body-changed entities
- expose the same parser through local-only provider commands that emit
  JSON/NDJSON snapshot, symbol, and relation records

## Licensing

The implementation does not copy or vendor Ataraxy Labs code. The runtime parser
dependency is `github.com/smacker/go-tree-sitter`, which is MIT-licensed. This
repository additionally vendors three tree-sitter grammars as source, each under
its own upstream MIT license: Dart (`internal/sem/grammars/dart/`), PostgreSQL
(`internal/sem/pgsql/`), and Zsh (`internal/sem/zsh/`).

## Current Limits

- Dependent counts are heuristic, not compiler/type-checker accurate.
- `CALLS`, `HANDLES_ROUTE`, and `HANDLES_TOOL` relations are heuristic.
- Rename detection is heuristic.
- Unsupported languages are reported as partial failures, not parsed semantically.
- The plugin is invoked as `entire graph ...`; it does not require changes to the
  main Entire CLI repository.
