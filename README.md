# 🧠 entire-graph

![License](https://img.shields.io/badge/license-MIT-blue)
![Languages](https://img.shields.io/badge/languages-36%20semantic%20%2B%20149%20inventory-2ea44f)
![Graph](https://img.shields.io/badge/graph-30%20relation%20types-8957e5)
![Local](https://img.shields.io/badge/100%25%20local-no%20egress-brightgreen)
![Deterministic](https://img.shields.io/badge/deterministic-no%20LLM%20·%20no%20vectors-f59e0b)
![Plugin](https://img.shields.io/badge/Entire%20CLI-plugin-24292f)

**A deterministic, local-first code graph for coding agents and for Entire checkpoints.** entire-graph parses your repository with tree-sitter and answers structural questions (where is this defined, who calls it, what changed at the entity level, what will this break) straight from the graph. No model calls, no embeddings, no network. The same commit always produces the same graph.

It ships as an **Entire CLI plugin**, invoked as `entire graph ...`, and doubles as a local-only semantic provider that streams a machine-readable graph of symbols and relations for downstream tools such as Entire Brain.

```sh
entire graph search   --repo . --query "where is webhook retry handled?"   # 🔍 ranked code for a task
entire graph snapshot --repo . --format ndjson                             # 🕸️  full symbol + relation graph
entire graph diff     --base main --head HEAD                               # 🧬 what changed, at the entity level
entire graph capabilities --json                                           # 🧭 languages + relation types
```

> 🔬 **Head to head.** On a frozen 21-repo multi-language board of fixed-answer semantic tasks (definition lookup, call graph, imports, change-impact), entire-graph scored **265/283 (94%)** versus **191/283 (68%)** for the leading tree-sitter code-memory tool. It wins where it matters: correct symbols, correct edges, correct impact.

---

## 🎯 Why entire-graph

- **🥇 More accurate than the leading code-memory tool.** 94% vs 68% on the same 21-repo board. entire-graph is built to be right about symbols and edges, not just fast at emitting them.
- **🎲 Deterministic. No LLM, no vectors.** Pure tree-sitter static analysis. The same commit always yields the same graph. No embeddings, no similarity thresholds, no fuzzy recall to second-guess. A relation is either in the graph or it is not.
- **🔒 100% local, zero egress.** No API keys, no network calls, no telemetry, no grammar downloads at runtime. `entire graph doctor --json` reports `no_egress=true`. Safe to point at private code.
- **🧭 Git-native and reproducible.** Every snapshot is keyed by `(repo, commit, tree)`, so the graph is content-addressed and cacheable per commit. Reads committed `HEAD` by default; pass `--worktree` for live edits.
- **⚡ Scales to millions of relations on one machine.** Measured builds: **41K relations in 4.8s**, **914K in 45s**, **2.5M in 130s**. No indexing service, no cluster.
- **🪶 Lean on memory where it counts.** Memory is released after the build. On repositories it fully indexes, peak stays in tens of MB where the competitor uses hundreds: **80 MB vs 599 MB** (nlohmann/json), **64 vs 285** (koin), **43 vs 225** (Dapper).
- **🗣️ 36 languages, one binary.** Deep semantic parsing for 36 languages plus 149 inventory-only filetypes, all from vendored tree-sitter grammars compiled in. Nothing to install at runtime.
- **🕸️ A 30-relation code graph with stable identity.** `compound-v1` symbol IDs survive ordinary edits. Relations span structure, calls, inheritance, fields, service boundaries, and config dependencies, each tagged with resolution and confidence.
- **💸 Fewer tokens for coding agents.** For a core symbol like `redisCommand` in redis, the five structural questions cost about **2,300 tokens** from the graph versus about **654,000 tokens** to read the 23 files that reference it: a **283x reduction** (measured with tiktoken). Savings scale with how widely a symbol is used.
- **🧩 Part of Entire.** The same graph powers entity-level checkpoint context, so a checkpoint can be judged (keep, revert, continue) without reading the full diff.

---

## 🚀 Quick Start

**Requirements:** Entire CLI · Git · Go toolchain with CGO (tree-sitter uses native parser bindings).

```sh
# Install the plugin binary and register it with Entire
go install github.com/entireio/entire-graph/cmd/entire-graph@main
entire plugin install "$(go env GOPATH)/bin/entire-graph" --force

# Verify install + local-only environment
entire graph version
entire graph doctor --json      # includes "no_egress": true

# First semantic diff, from any git repo
entire graph commit HEAD
```

If `$(go env GOPATH)/bin` is on your `PATH`, Entire can discover the binary directly after `go install`. Entire plugins are local executables: `entire graph` works because Entire finds an `entire-graph` binary in its managed plugin directory or on `$PATH`.

### 🛠️ Install From Source

```sh
git clone https://github.com/entireio/entire-graph.git
cd entire-graph
mise run build
entire plugin install ./entire-graph --force
```

One-command local install: `scripts/install-local.sh`. Release archives with `SHA256SUMS`: `scripts/release.sh`. See [docs/operations.md](docs/operations.md) for target and cgo details.

---

## 🧩 Commands

| Command | What it does |
|---|---|
| `entire graph commit <rev>` | Entity-level semantic diff of a commit vs its first parent |
| `entire graph diff --base <a> --head <b>` | Semantic diff between two refs (`analyze` is an alias) |
| `entire graph checkpoint <id>` | Semantic diff for the commit behind an Entire checkpoint trailer |
| `entire graph search --query "..."` | 🔍 Ranked source regions for a task description |
| `entire graph snapshot --format ndjson` | 🕸️ Full graph stream: header + files + symbols + relations |
| `entire graph symbols --format ndjson` | Symbols only (filtered snapshot) |
| `entire graph edges --format ndjson` | Relations only (filtered snapshot) |
| `entire graph capabilities --json` | Languages, relation types, features, network requirements |
| `entire graph doctor --json` | Environment, plugin data dir, `no_egress=true` |
| `entire graph version --json` | Provider name and plugin version |

All diff commands print human-readable text by default and structured output with `--json`:

```text
Semantic changes HEAD~1..HEAD

auth.py
  ~ function validate_token signature changed (14 dependents)
  + class TokenClaims added
  - function parse_token removed (0 dependents)
```

---

## 🔍 Search

```sh
entire graph search --repo . --query "retry logic for webhook delivery" --top-k 20 --format json
```

Search fuses source-body matching, identifier splitting, symbol names and signatures, paths, conceptual issue-to-API terms, and diversity-aware region selection. It can return several non-overlapping regions from one file, each with broad provenance plus a focused snippet.

- Reads the **working tree by default** so an agent sees its own dirty edits. Use `--head` for committed-tree semantics.
- Output is budgeted to **16 KiB** of serialized snippets by default, sized to drop straight into a model context window. Tune with `--top-k` and `--max-context-bytes`.
- Cold search scans files without parsing, then indexes at most 96 query-relevant files (`--max-indexed-files`, or `--index-all-files`). The default `syntax-only` profile avoids whole-repo graph construction; `--profile fast` adds local relation expansion.
- When Entire supplies `ENTIRE_PLUGIN_DATA_DIR`, committed-tree searches reuse a tree-keyed compressed index across invocations.

---

## 🕸️ The Code Graph

**Node kinds:** Project, Package, Folder, File, Module, Class, Function, Method, Interface, Enum, Type, plus service entities like Route and Resource, and YAML workflow sections / GitHub Actions jobs.

**Schema 1.1 defines 30 relation types**, emitted in profile-scoped subsets:

| Group | Relations |
|---|---|
| 🏗️ Structural | `DEFINES` · `CONTAINS` · `IMPORTS` |
| 📞 Calls & construction | `CALLS` · `CONSTRUCTS` · `ASYNC_CALLS` |
| 🧬 Types & inheritance | `EXTENDS` · `INHERITS` · `IMPLEMENTS` · `OVERRIDES` · `USES_TYPE` · `PARAM_TYPE` · `RETURNS_TYPE` |
| 🗂️ Fields | `READS_FIELD` · `WRITES_FIELD` · `ACCESSES` |
| 🌐 Service boundaries | `HANDLES_ROUTE` · `HANDLES_GRPC` · `HANDLES_GRAPHQL` · `HANDLES_TRPC` · `HTTP_CALLS` · `EMITS` · `LISTENS_ON` · `HANDLES_TOOL` |
| ⚙️ Config & analysis | `CONFIGURES` · `SIMILAR_TO` · `TESTS` · `RESOURCE_DEPENDS_ON` · `DATA_FLOWS` · `FILE_CHANGES_WITH` |

Indexing profiles: `syntax-only` emits `DEFINES` and `CONTAINS`; `fast` adds imports, shallow calls, and boundary relations; `full` emits every relation type with evidence fields. `capabilities --json` reports the supported relation types per language.

**Stable identity.** `compound-v1` symbol IDs use repo key, language, path, kind, and qualified name, so they survive ordinary body and signature edits. Duplicate same-name symbols in one file are disambiguated by source range.

---

## 🗣️ Language Support

**36 languages with deep semantic parsing:** Bash, C, C++, C#, Clojure/ClojureScript, CUE, Dart, Elixir, Erlang, F#, Go, Groovy, Haskell, HCL/Terraform, Java, JavaScript, Julia, Kotlin, Lua, Objective-C, OCaml, Perl, PHP, Protocol Buffers, Python, R, Ruby, Rust, Scala, SQL, Swift, TypeScript, YAML (including GitHub Actions workflow sections and jobs), Zig, and Zsh.

Another **149 filetypes** are recognized as inventory-only: stable file records without semantic claims. Everything else is reported as an honest partial failure rather than dropped silently. See [docs/language-support.md](docs/language-support.md) for the full two-tier matrix.

---

## 🤖 Agent Usage

entire-graph is built to be called by coding agents, not only humans. Every command is local-only and no-egress, so agents can run it freely inside sandboxed sessions.

- **🔍 Find where to work.** Retrieve ranked regions for a task before editing:
  ```sh
  entire graph search --repo . --query "retry logic for webhook delivery" --format json
  ```
- **🧬 Judge a change.** Summarize what actually changed at the entity level to decide keep / revert / continue:
  ```sh
  entire graph commit HEAD --json
  ```
  Signature changes with high dependent counts are a strong signal to run tests first.
- **🕸️ Build a code graph.** Ingest machine-readable symbols and relations into agent memory or a store such as Entire Brain:
  ```sh
  entire graph snapshot --repo . --format ndjson
  ```
- **🧭 Feature-detect first.** `entire graph capabilities --json` lists semantic vs inventory-only languages and supported relation types; `doctor --json` verifies the environment, including `no_egress=true`.

Inside Entire sessions the CLI sets `ENTIRE_REPO_ROOT` and `ENTIRE_PLUGIN_DATA_DIR` automatically. Outside Entire, pass `--repo`. See [AGENTS.md](AGENTS.md) for repo-specific agent instructions.

---

## 📊 Benchmarks

All figures below are measured, not estimated.

### 🎯 Accuracy vs codebase-memory-mcp

Fixed-answer semantic tasks (definition lookup, call graph, imports, change-impact) on real checkouts, scored as correctness counts.

| System | Score | Corpus |
|---|---|---|
| **entire-graph** | **265 / 283 (94%)** | 21 repos, multi-language |
| codebase-memory-mcp | 191 / 283 (68%) | same board |

### ⚡ Throughput

| Repo | Relations | Build time |
|---|--:|--:|
| entire-api | 41,106 | 4.8s |
| entiredb | 913,801 | 45s |
| entire.io | 2,549,268 | 130s |

### 🪶 Peak memory (vs codebase-memory-mcp)

| Repo | entire-graph | codebase-memory-mcp |
|---|--:|--:|
| nlohmann/json | **80 MB** | 599 MB |
| koin | **64 MB** | 285 MB |
| Dapper | **43 MB** | 225 MB |

Memory is released after the build completes.

### 💸 Token efficiency

Answering the five structural questions for a core symbol, graph query vs reading every referencing file (tiktoken `cl100k`):

| Task | entire-graph | file-by-file | Reduction |
|---|--:|--:|--:|
| `redisCommand` (redis), 23 referencing files | ~2,300 tokens | ~654,000 tokens | **283x** |

Savings scale with symbol connectivity: single-digit for narrow symbols, 280x+ for core symbols.

---

## 🔒 Local-First and Private

entire-graph runs entirely on your machine. It makes **no network calls**, ships with **no telemetry**, needs **no API keys**, and downloads **no grammars at runtime** (they are compiled into the binary). `entire graph doctor --json` reports `no_egress=true`, and `--no-network` makes the no-egress contract explicit to callers. Your source never leaves your machine.

---

## 🧬 How It Works

The plugin shells out to git for trees and diffs, parses before/after states with tree-sitter behind `internal/sem`, reconciles renames and moves by similarity, and streams results so memory stays bounded on very large repositories.

Unlike code tools that embed an LLM for natural-language-to-query translation, entire-graph has **no model inside it**. Your coding agent is already the intelligence layer: it calls `search`, `snapshot`, or `diff`, and reasons over the structured results. That means no extra keys, no extra cost, and a graph you can trust to be the same every run.

---

## 📦 Provider Contract (NDJSON)

For Entire and other local tools that want a stable graph rather than human diff text:

- `snapshot --format ndjson` emits a header record plus file, external-endpoint, symbol, and relation records.
- `symbols` / `edges` emit the same header followed by their record type.
- `search --query ...` emits ranked, qrel-blind source regions as JSON, NDJSON, or text, bounded to 16 KiB by default.

Snapshot headers carry schema version, provider version, repo key, `HEAD` commit and tree, parsed languages, capability labels, warnings, partial failures, and completeness stats. Symbol records carry stable compound IDs, kind, qualified name, source range, signature, body hash, language, and container ID. Unsupported-but-detected files and files with syntax errors are reported as machine-readable partial failures; recoverable symbols are still emitted.

Run without Entire:

```sh
ENTIRE_REPO_ROOT=/path/to/repo ./entire-graph diff --base HEAD~1 --head HEAD
```

---

## ⚠️ Current Limits

- Dependent counts are heuristic, not compiler/type-checker accurate.
- `CALLS`, `HANDLES_ROUTE`, and `HANDLES_TOOL` relations are heuristic.
- Rename detection is heuristic.
- Unsupported languages are reported as partial failures, not parsed semantically.
- The plugin is invoked as `entire graph ...`; it requires no changes to the main Entire CLI.

---

## 📄 License

MIT. The runtime parser dependency is `github.com/smacker/go-tree-sitter` (MIT). Three tree-sitter grammars are vendored as source under their own upstream MIT licenses: Dart (`internal/sem/grammars/dart/`), PostgreSQL (`internal/sem/pgsql/`), and Zsh (`internal/sem/zsh/`). This implementation does not copy or vendor Ataraxy Labs code.

Issue [entireio/cli#589](https://github.com/entireio/cli/issues/589) proposes entity-level checkpoint context instead of stopping at "this file changed." entire-graph is a plugin-shaped implementation of that idea.
