# 🧠 entire-graph

![License](https://img.shields.io/badge/license-MIT-blue)
![Languages](https://img.shields.io/badge/languages-36%20semantic%20%2B%20149%20inventory-2ea44f)
![Graph](https://img.shields.io/badge/graph-30%20relation%20types-8957e5)
![Local](https://img.shields.io/badge/100%25%20local-no%20egress-brightgreen)
![Deterministic](https://img.shields.io/badge/deterministic-no%20LLM%20·%20no%20vectors-f59e0b)
![Accuracy](https://img.shields.io/badge/accuracy-94%25%20on%2021%20repos-success)
![Plugin](https://img.shields.io/badge/Entire%20CLI-plugin-24292f)

**A deterministic, local-first code graph for coding agents, and the semantic layer behind Entire checkpoints.** entire-graph parses your repository with tree-sitter and answers structural questions (where is this defined, who calls it, what changed at the entity level, what will this break) straight from a persistent knowledge graph of functions, classes, call chains, routes, and cross-service links. No model calls, no embeddings, no network. The same commit always produces the same graph.

It ships as an **Entire CLI plugin**, invoked as `entire graph ...`, and doubles as a local-only semantic provider that streams a machine-readable graph of symbols and relations for downstream tools such as Entire Brain. High-quality tree-sitter parsing across **36 semantic languages** and **149 inventory filetypes**, a **30-relation** graph with stable identity, and agent-ready hybrid search, all from a single plugin binary with zero runtime dependencies beyond git.

```sh
entire graph search   --repo . --query "where is webhook retry handled?"   # 🔍 ranked code for a task
entire graph snapshot --repo . --format ndjson                             # 🕸️  full symbol + relation graph
entire graph diff     --base main --head HEAD                               # 🧬 what changed, at the entity level
entire graph capabilities --json                                           # 🧭 languages + relation types
```

> 🔬 **Accuracy first.** On a frozen 21-repo multi-language board of fixed-answer semantic tasks (definition lookup, call graph, imports, change-impact) scored on real checkouts, entire-graph scored **265/283 (94%)** versus **191/283 (68%)** for the leading tree-sitter code-memory tool. A wrong edge is worse than a missing one, so the graph is built to be right about symbols and impact, not just fast at emitting them.
>
> 🔒 **Security and trust.** entire-graph reads your codebase and writes only to Entire's managed plugin directory. All processing happens 100% locally: no network, no telemetry, no API keys, no grammar downloads at runtime. Your code never leaves your machine. `entire graph doctor --json` reports `no_egress=true`.

---

## 📑 Table of Contents

- [🎯 Why entire-graph](#-why-entire-graph)
- [🚀 Quick Start](#-quick-start)
- [🧩 Commands](#-commands)
- [🔍 Search](#-search)
- [🧬 Semantic Diff](#-semantic-diff)
- [🕸️ The Code Graph](#️-the-code-graph)
- [🗣️ Language Support](#️-language-support)
- [🤖 Agent Usage](#-agent-usage)
- [📊 Benchmarks](#-benchmarks)
- [⚡ Performance](#-performance)
- [🧬 How It Works](#-how-it-works)
- [📦 Provider Contract](#-provider-contract-ndjson)
- [🗂️ Ignoring Files](#️-ignoring-files)
- [⚙️ Configuration](#️-configuration)
- [🏗️ Architecture](#️-architecture)
- [🧪 Benchmark Harness](#-benchmark-harness)
- [⚠️ Current Limits](#️-current-limits)
- [📄 License](#-license)

---

## 🎯 Why entire-graph

- **🥇 More accurate than the leading code-memory tool.** 94% vs 68% on the same 21-repo board of definition-lookup, call-graph, imports, and change-impact tasks. Correctness is the product.
- **🎲 Deterministic. No LLM, no vectors.** Pure tree-sitter static analysis. The same commit always yields the same graph. No embeddings, no similarity thresholds, no fuzzy recall to second-guess. A relation is either in the graph or it is not.
- **🔒 100% local, zero egress.** No API keys, no network calls, no telemetry, no grammar downloads at runtime. `entire graph doctor --json` reports `no_egress=true`. Safe to point at private code.
- **🧭 Git-native and reproducible.** Every snapshot is keyed by `(repo, commit, tree)`, so the graph is content-addressed and cacheable per commit. Reads committed `HEAD` by default; pass `--worktree` for live edits.
- **⚡ Scales to millions of relations on one machine.** Real builds: redis at 358K relations in 5.5s, laravel at 1.87M in 17s, micropython at 2.27M in 25.6s. Semantic diffs return in about 0.1s. No indexing service, no cluster.
- **🪶 Lean on memory where it counts.** Memory is released after the build. On repositories it fully indexes, peak stays in tens of MB where the competitor uses hundreds: **80 MB vs 599 MB** (nlohmann/json), **64 vs 285** (koin), **43 vs 225** (Dapper).
- **🗣️ 36 semantic languages, one binary.** Deep parsing for 36 languages plus 149 inventory-only filetypes (185 recognized names total), all from vendored tree-sitter grammars compiled in. Nothing to install at runtime, nothing that breaks.
- **🕸️ A 30-relation code graph with stable identity.** `compound-v1` symbol IDs survive ordinary edits. Relations span structure, calls, inheritance, fields, service boundaries, and config dependencies, each tagged with resolution and confidence.
- **💸 Fewer tokens for coding agents.** For a core symbol like `redisCommand` in redis, the five structural questions cost about **2,300 tokens** from the graph versus about **654,000 tokens** to read the 23 files that reference it: a **283x reduction** (measured with tiktoken). Savings scale with how widely a symbol is used.
- **🧩 Part of Entire.** The same graph powers entity-level checkpoint context, so a checkpoint can be judged (keep, revert, continue) without reading the full diff.

---

## 🚀 Quick Start

**Requirements:** Entire CLI · Git · Go toolchain with CGO enabled (tree-sitter uses native parser bindings).

```sh
# 1. Install the plugin binary and register it with Entire's managed plugin dir
go install github.com/entireio/entire-graph/cmd/entire-graph@main
entire plugin install "$(go env GOPATH)/bin/entire-graph" --force

# 2. Verify the install and the local-only environment
entire graph version
entire graph doctor --json        # includes "no_egress": true

# 3. Run your first semantic diff, from any git repository
entire graph commit HEAD
```

If `$(go env GOPATH)/bin` is already on your `PATH`, Entire can discover the binary directly after `go install`. Entire plugins are local executables, not a hosted marketplace: `entire graph` works because Entire finds an `entire-graph` binary in its managed plugin directory or on `$PATH`.

### 🛠️ Install From Source

```sh
git clone https://github.com/entireio/entire-graph.git
cd entire-graph
mise run build
entire plugin install ./entire-graph --force
```

For a one-command local source install run `scripts/install-local.sh`; it builds `./entire-graph`, installs it, prints `entire graph version`, and fails before writing anything if the parent `entire` CLI is not on `PATH`. For release archives with `SHA256SUMS`, run `scripts/release.sh`. See [docs/operations.md](docs/operations.md) for target and cgo details.

---

## 🧩 Commands

| Command | What it does |
|---|---|
| `entire graph commit <rev>` | Entity-level semantic diff of a commit against its first parent |
| `entire graph diff --base <a> --head <b>` | Semantic diff between two arbitrary refs (`analyze` is an alias) |
| `entire graph checkpoint <id>` | Semantic diff for the commit behind an Entire checkpoint trailer |
| `entire graph search --query "..."` | 🔍 Ranked source regions for a natural-language task |
| `entire graph snapshot --format ndjson` | 🕸️ Full graph stream: header + files + symbols + relations |
| `entire graph symbols --format ndjson` | Symbols only (filtered snapshot view) |
| `entire graph edges --format ndjson` | Relations only (filtered snapshot view) |
| `entire graph capabilities --json` | Languages, relation types, features, network requirements |
| `entire graph doctor --json` | Environment, repo resolution, plugin data dir, `no_egress=true` |
| `entire graph version --json` | Provider name and plugin version |

Every diff command prints human-readable text by default and structured output with `--json`:

```text
Semantic changes HEAD~1..HEAD

auth.py
  ~ function validate_token signature changed (14 dependents)
  + class TokenClaims added
  - function parse_token removed (0 dependents)
```

Run any command without going through Entire by setting the repo root explicitly:

```sh
ENTIRE_REPO_ROOT=/path/to/repo ./entire-graph diff --base HEAD~1 --head HEAD
```

---

## 🔍 Search

```sh
entire graph search --repo . --query "retry logic for webhook delivery" --top-k 20 --format json
```

Search returns ranked, diverse source regions for a task description, fusing several signals:

- source-body matching and identifier splitting (camelCase / snake_case aware)
- symbol names and signatures, and file paths
- conceptual issue-to-API term expansion
- graph-neighbor expansion (in the `fast` profile) so related regions surface together
- diversity-aware region selection, so one file can contribute several non-overlapping regions

Behavior and budgets:

- **Working tree by default**, so an agent sees its own dirty edits. Use `--head` for immutable committed-tree semantics.
- **Bounded output.** Results are budgeted to **16 KiB** of serialized snippets by default, sized to drop straight into a model context window. Tune with `--top-k` and `--max-context-bytes` (`--max-context-bytes 0` for an unbounded diagnostic ranking).
- **Cold search** scans files without parsing, then indexes at most 96 query-relevant files. Change the bound with `--max-indexed-files`, or force exhaustive parsing with `--index-all-files`.
- **Profiles.** The default `syntax-only` profile avoids synchronous whole-repository graph construction; `--profile fast` adds local relation expansion when deeper semantic indexing is worth the cost.
- **Caching.** When Entire supplies `ENTIRE_PLUGIN_DATA_DIR`, committed-tree searches reuse a tree-keyed compressed index across invocations. Direct callers can set `--cache-dir`; `--no-cache` disables persistence. Worktree searches never reuse committed indexes, avoiding stale results after edits.

---

## 🧬 Semantic Diff

`commit`, `diff`, and `checkpoint` report what changed at the entity level, not just which files changed:

- extract named entities (functions, classes, methods, structs, traits, types, YAML workflow sections, GitHub Actions jobs) from the before and after git trees
- compare signatures and normalized bodies
- classify each entity as **added**, **removed**, **renamed**, **signature-changed**, or **body-changed**
- attach a heuristic **dependent count** from parsed references in the target tree, so a signature change with many dependents stands out as risky
- reconcile renames and moves by similarity

This is what lets an agent, or a human, decide whether a checkpoint is safe to keep, revert, or continue from, without reading the full textual diff. Signature changes with high dependent counts are a strong signal to run tests first.

---

## 🕸️ The Code Graph

**Node kinds:** Project, Package, Folder, File, Module, Class, Function, Method, Interface, Enum, Type, plus service entities such as Route and Resource, and YAML workflow sections / GitHub Actions jobs.

**Schema 1.1 defines 30 relation types**, emitted in profile-scoped subsets:

| Group | Relations |
|---|---|
| 🏗️ Structural | `DEFINES` · `CONTAINS` · `IMPORTS` |
| 📞 Calls & construction | `CALLS` · `CONSTRUCTS` · `ASYNC_CALLS` |
| 🧬 Types & inheritance | `EXTENDS` · `INHERITS` · `IMPLEMENTS` · `OVERRIDES` · `USES_TYPE` · `PARAM_TYPE` · `RETURNS_TYPE` |
| 🗂️ Fields | `READS_FIELD` · `WRITES_FIELD` · `ACCESSES` |
| 🌐 Service boundaries | `HANDLES_ROUTE` · `HANDLES_GRPC` · `HANDLES_GRAPHQL` · `HANDLES_TRPC` · `HTTP_CALLS` · `EMITS` · `LISTENS_ON` · `HANDLES_TOOL` |
| ⚙️ Config & analysis | `CONFIGURES` · `SIMILAR_TO` · `TESTS` · `RESOURCE_DEPENDS_ON` · `DATA_FLOWS` · `FILE_CHANGES_WITH` |

**Indexing profiles** trade depth for speed: `syntax-only` emits `DEFINES` and `CONTAINS`; `fast` adds imports, shallow calls, boundary relations, and infrastructure-as-code edges; `full` (default) emits every relation type with evidence fields. `capabilities --json` reports the supported relation types per language.

**Cross-service and infrastructure.** Service boundaries (REST routes, gRPC, GraphQL, tRPC, pub/sub channels) are first-class edges, and Dockerfiles, Kubernetes manifests, and Terraform / HCL are indexed as graph nodes with dependency edges.

**Stable identity.** `compound-v1` symbol IDs use repo key, language, path, kind, and qualified name, so they survive ordinary body and signature edits. Duplicate same-name symbols in one file are disambiguated by source range.

---

## 🗣️ Language Support

`capabilities --json` exposes two tiers so evaluations stay honest: `semantic_languages` (parser-backed call/type/data-flow analysis) and `inventory_only_languages` (stable file and document records without semantic claims). Current counts: **185 recognized names, 36 semantic, 149 inventory-only.**

**36 semantic languages:** Bash, C, C#, C++, CUE, Clojure, ClojureScript, Dart, Elixir, Erlang, F#, Go, Groovy, HCL/Terraform, Haskell, Java, JavaScript, Julia, Kotlin, Lua, OCaml, Objective-C, PHP, Perl, Protocol Buffers, Python, R, Ruby, Rust, SQL, Scala, Swift, TypeScript, YAML (including GitHub Actions workflow sections and jobs), Zig, Zsh.

Another **149 filetypes** are recognized as inventory-only, and everything else is reported as an honest partial failure rather than dropped silently. See [docs/language-support.md](docs/language-support.md) for the full two-tier matrix. Inventory-only coverage is never counted as semantic parity in benchmarks.

---

## 🤖 Agent Usage

entire-graph is built to be called by coding agents, not only humans. Every command is local-only and no-egress, so agents can run it freely inside sandboxed sessions.

- **🔍 Find where to work.** Retrieve ranked regions for a task before editing:
  ```sh
  entire graph search --repo . --query "retry logic for webhook delivery" --format json
  ```
  Output is bounded to 16 KiB by default, sized for direct inclusion in a model context window.
- **🧬 Judge a change.** Summarize what actually changed at the entity level to decide keep / revert / continue:
  ```sh
  entire graph commit HEAD --json
  ```
- **🕸️ Build a code graph.** Ingest machine-readable symbols and relations into agent memory or a store such as Entire Brain:
  ```sh
  entire graph snapshot --repo . --format ndjson
  ```
  Records carry stable `compound-v1` IDs, so entities can be tracked across sessions and ordinary edits.
- **🧭 Feature-detect first.** `entire graph capabilities --json` lists semantic vs inventory-only languages and supported relation types; `doctor --json` verifies the environment, including `no_egress=true`.

Inside Entire sessions the CLI sets `ENTIRE_REPO_ROOT` and `ENTIRE_PLUGIN_DATA_DIR` automatically, and committed-tree searches reuse a tree-keyed compressed index across invocations. Outside Entire, pass `--repo`. See [AGENTS.md](AGENTS.md) for repo-specific agent instructions.

---

## 📊 Benchmarks

All figures below are measured on real, public repositories, not estimated.

### 🎯 Accuracy vs codebase-memory-mcp

Fixed-answer semantic tasks (definition lookup, call graph, imports, change-impact) on real checkouts, scored as correctness counts (right symbol / file / edge).

| System | Score | Corpus |
|---|---|---|
| **entire-graph** | **265 / 283 (94%)** | 21 repos, multi-language |
| codebase-memory-mcp | 191 / 283 (68%) | same board |

### ⚡ Throughput (full profile)

| Repo | Language | Files | Symbols | Relations | Build time |
|---|---|--:|--:|--:|--:|
| caddyserver/caddy | Go | 214 | 1,857 | 23,681 | 1.5s |
| redis/redis | C | 633 | 10,312 | 358,082 | 5.5s |
| laravel/framework | PHP | 2,019 | 23,182 | 1,872,087 | 17.0s |
| micropython/micropython | C | 3,447 | 18,788 | 2,268,173 | 25.6s |

### 🪶 Peak memory (vs codebase-memory-mcp)

| Repo | entire-graph | codebase-memory-mcp |
|---|--:|--:|
| nlohmann/json | **80 MB** | 599 MB |
| koin | **64 MB** | 285 MB |
| Dapper | **43 MB** | 225 MB |

Memory is released back to the OS after the build completes.

### 💸 Token efficiency

Answering the five structural questions for a core symbol, graph query vs reading every referencing file (tiktoken `cl100k`):

| Task | entire-graph | file-by-file | Reduction |
|---|--:|--:|--:|
| `redisCommand` (redis), 23 referencing files | ~2,300 tokens | ~654,000 tokens | **283x** |

Savings scale with symbol connectivity: single-digit for narrow symbols, 280x+ for core symbols.

Every number here is reproducible with the benchmark harness (see [Benchmark Harness](#-benchmark-harness) and [docs/benchmarks.md](docs/benchmarks.md)).

---

## ⚡ Performance

- **Semantic diff:** about 0.1s for a typical `HEAD~1..HEAD` on redis.
- **Full-graph build:** linear in repository size; 23K relations in 1.5s up to 2.27M relations in 25.6s across the repos above.
- **Streaming output:** `snapshot` emits records as it parses, so memory stays bounded on very large repositories rather than materializing the whole graph in RAM.
- **Cached committed-tree search:** reuses a tree-keyed compressed index across invocations when `ENTIRE_PLUGIN_DATA_DIR` is set, so repeated queries on an unchanged tree skip re-parsing.

Absolute numbers are environment-sensitive (measured on Apple Silicon). Read them as relative signals and reproduce locally with the harness.

---

## 🧬 How It Works

The plugin shells out to git for trees and diffs, parses before/after states with tree-sitter behind `internal/sem`, reconciles renames and moves by similarity, and streams results so memory stays bounded on very large repositories. Indexing runs as a multi-pass pipeline: structure, then definitions, then calls, then service/HTTP links, then config and infrastructure, then tests.

Unlike code tools that embed an LLM for natural-language-to-query translation, entire-graph has **no model inside it**. Your coding agent is already the intelligence layer: it calls `search`, `snapshot`, or `diff`, and reasons over the structured results. That means no extra keys, no extra cost, and a graph you can trust to be identical on every run.

```text
You:   "what calls processOrder?"
Agent: entire graph edges --repo . --format ndjson   (then filters to processOrder)
Graph: returns the exact caller symbols with file:line
Agent: presents the call chain in plain English
```

---

## 📦 Provider Contract (NDJSON)

For Entire and other local tools that want a stable graph rather than human diff text:

- `snapshot --format ndjson` emits one header record, then file, external-endpoint, symbol, and relation records.
- `symbols` / `edges` emit the same header followed by their record type.
- `search --query ...` emits ranked, qrel-blind source regions as JSON, NDJSON, or text, bounded to 16 KiB by default.
- `version`, `doctor`, and `capabilities` describe the provider, environment, and supported features.

Snapshot headers carry the schema version, provider version, repository key, `HEAD` commit and tree when available, parsed languages, capability labels, warnings, partial failures, and aggregate completeness stats. File records include stable file IDs so `DEFINES` endpoints resolve. Symbol records include stable compound IDs, `stable_id_version`, kind, qualified name, source range, signature, body hash, language, and container ID when nested. External-endpoint records describe relation targets such as imported modules, routes, and tool handlers.

Unsupported-but-detected source files are reported as machine-readable partial failures instead of disappearing silently. Supported files with tree-sitter syntax errors are also reported as partial failures while any recoverable symbols are still emitted. Repositories without a readable `HEAD` fall back to the working tree and include a warning in the header. Pass `--no-network` to make the no-egress contract explicit to callers.

---

## 🗂️ Ignoring Files

Snapshots honor the repository root `.gitignore` by default. Two repeatable flags refine the set, both using gitignore syntax:

- `--ignore-file <path>` adds extra exclusions, resolved relative to `--repo` unless absolute.
- `--include-file <path>` adds inclusion rules applied after `.gitignore` and `--ignore-file`, so callers can reopen otherwise-ignored paths.

By default snapshots read the committed `HEAD` tree, so dirty tracked edits and untracked files are excluded; `--worktree` includes live working-tree contents.

---

## ⚙️ Configuration

Environment variables and flags that shape a run:

| Variable / flag | Effect |
|---|---|
| `ENTIRE_REPO_ROOT` | Repository root when running outside Entire (or use `--repo`) |
| `ENTIRE_PLUGIN_DATA_DIR` | Enables the tree-keyed compressed index cache for committed-tree search |
| `--worktree` | Index live working-tree contents instead of committed `HEAD` |
| `--no-network` | Make the no-egress contract explicit to callers |
| `--profile syntax-only\|fast\|full` | Relation depth (structure only → shallow → complete) |
| `--cache-dir` / `--no-cache` | Set or disable the search index cache directory |
| `--max-indexed-files` / `--index-all-files` | Bound or exhaust cold-search parsing |
| `--top-k` / `--max-context-bytes` | Search result count and byte budget |
| `--ignore-file` / `--include-file` | Extra gitignore-style exclude / include rules |

---

## 🏗️ Architecture

```text
cmd/
  entire-graph/     Plugin entry point (semantic diff, search, provider commands)
  graph-bench/      Reproducible benchmark driver
internal/
  sem/              Tree-sitter parsing, symbol + relation extraction, rename reconciliation
  bench/            Measurement core (throughput, memory, coverage)
docs/               Language support matrix, benchmarks, operations, provider requirements
scripts/            Local install and checksum-backed release archives
```

The parser is isolated behind `internal/sem`, so the command surface stays stable while the semantic model gets richer.

---

## 🧪 Benchmark Harness

Every performance and quality number is reproducible with `cmd/graph-bench`:

```sh
# Pin repo commits once (writes bench/repos.lock.json), then commit the result
go run ./cmd/graph-bench -update-lock

# Fast tier: routine per-phase tracking (minutes)
go run ./cmd/graph-bench -manifest bench/repos.fast.json

# Full tier: all languages x repos (slow; includes mega-repos)
go run ./cmd/graph-bench

# Quick subset / offline
go run ./cmd/graph-bench -languages Go,Rust -limit 3
go run ./cmd/graph-bench -profile syntax-only -languages Go -limit 1
```

Read speed and throughput from the `fast` profile (and `syntax-only` for the floor), and semantic depth and coverage from `full`. See [docs/benchmarks.md](docs/benchmarks.md) for the full harness layout, flags, and guardrails.

---

## ⚠️ Current Limits

- Dependent counts are heuristic, not compiler / type-checker accurate.
- `CALLS`, `HANDLES_ROUTE`, and `HANDLES_TOOL` relations are heuristic.
- Rename detection is heuristic.
- Unsupported languages are reported as partial failures, not parsed semantically.
- The plugin is invoked as `entire graph ...`; it requires no changes to the main Entire CLI.

---

## 📄 License

MIT. The runtime parser dependency is `github.com/smacker/go-tree-sitter` (MIT). Three tree-sitter grammars are vendored as source under their own upstream MIT licenses: Dart (`internal/sem/grammars/dart/`), PostgreSQL (`internal/sem/pgsql/`), and Zsh (`internal/sem/zsh/`). This implementation does not copy or vendor Ataraxy Labs code.
