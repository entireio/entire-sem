# 🧠 entire-graph

![License](https://img.shields.io/badge/license-MIT-blue)
![Languages](https://img.shields.io/badge/languages-36%20semantic%20%2B%20149%20inventory-2ea44f)
![Graph](https://img.shields.io/badge/graph-30%20relation%20types-8957e5)
![Local](https://img.shields.io/badge/100%25%20local-no%20egress-brightgreen)
![Deterministic](https://img.shields.io/badge/deterministic-no%20LLM%20·%20no%20vectors-f59e0b)
![Accuracy](https://img.shields.io/badge/accuracy-94%25%20on%2021%20repos-success)
![Plugin](https://img.shields.io/badge/Entire%20CLI-plugin-24292f)

**A deterministic, local-first code graph for coding agents, and the semantic layer behind Entire checkpoints.** entire-graph parses your repository with tree-sitter and answers structural questions — where is this defined, who calls it, what changed at the entity level, what will this break — straight from a persistent knowledge graph of functions, classes, call chains, routes, and cross-service links. No model calls, no embeddings, no network. The same commit always produces the same graph.

It ships as an **Entire CLI plugin**, invoked as `entire graph ...`, and doubles as a local-only semantic provider that streams a machine-readable graph of symbols and relations for downstream tools such as Entire Brain.

```sh
entire graph search   --repo . --query "where is webhook retry handled?"   # 🔍 ranked code for a task
entire graph snapshot --repo . --format ndjson                             # 🕸️  full symbol + relation graph
entire graph diff     --base main --head HEAD                               # 🧬 what changed, at the entity level
entire graph capabilities --json                                           # 🧭 languages + relation types
```

> 🔬 **Accuracy first.** On a frozen 21-repo multi-language board of fixed-answer semantic tasks (definition lookup, call graph, imports, change-impact) scored on real checkouts, entire-graph scored **265/283 (94%)** versus **191/283 (68%)** for the leading tree-sitter code-memory tool. A wrong edge is worse than a missing one, so the graph is built to be right about symbols and impact, not just fast at emitting them.
>
> 🔒 **Security and trust.** entire-graph reads your codebase and writes only to Entire's managed plugin directory. All processing happens 100% locally: no network, no telemetry, no API keys, no grammar downloads at runtime. Your code never leaves your machine. `entire graph doctor --json` reports `no_egress=true`.

> **Are you a coding agent (or configuring one)?** The operating instructions — the parts of the graph, the exact commands, and the query-before-grep doctrine — live in **[AGENTS.md](AGENTS.md)** (mirrored in [CLAUDE.md](CLAUDE.md)). This README is for humans installing and running the plugin.

---

## 📑 Contents

- [What it is](#-what-it-is)
- [Install](#-install)
- [MCP & Entire Brain integration](#-mcp--entire-brain-integration)
- [Refresh & keeping the graph current](#-refresh--keeping-the-graph-current)
- [Quick Start](#-quick-start)
- [Commands](#-commands)
- [Language support](#️-language-support)
- [Benchmarks](#-benchmarks)
- [Performance](#-performance)
- [Security & local-first](#-security--local-first)
- [Architecture](#️-architecture)
- [Current limits](#️-current-limits)
- [License](#-license)

---

## 🎯 What it is

entire-graph builds a **code graph** — nodes for files, packages, functions, classes, methods, types, routes, and resources; **30 relation types** for calls, construction, inheritance, field access, service boundaries, and config/infra dependencies — and answers structural questions from it:

- **Where is this defined?** → symbols
- **Who calls this / what does it call?** → neighbors, edges
- **Find the code for this task** → search (hybrid, ranked)
- **What changed, and what does it put at risk?** → diff / commit / checkpoint
- **Give me the whole graph** → snapshot

Everything is **deterministic** (pure tree-sitter static analysis — no LLM, no vectors, no similarity thresholds), **git-native** (every result keyed by `(repo, commit, tree)`, so it is content-addressed and cacheable per commit), and **100% local** (no keys, no network, no telemetry). It scales to millions of relations on one machine and releases memory after each build.

---

## 🚀 Install

**Prerequisites:** the Entire CLI · Git · a Go toolchain with **CGO enabled** (tree-sitter uses native parser bindings).

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

Entire plugins are local executables, not a hosted marketplace: `entire graph` works because Entire finds an `entire-graph` binary in its managed plugin directory or on `$PATH`. If `$(go env GOPATH)/bin` is already on your `PATH`, Entire can discover the binary directly after `go install`.

### 🛠️ Install from source

```sh
git clone https://github.com/entireio/entire-graph.git
cd entire-graph
mise run build                        # go build -o entire-graph ./cmd/entire-graph
entire plugin install ./entire-graph --force
```

`scripts/install-local.sh` does this in one command (builds, installs, prints `entire graph version`, and fails early if the parent `entire` CLI is not on `PATH`). For release archives with `SHA256SUMS`, run `scripts/release.sh`. See [docs/operations.md](docs/operations.md) for target and cgo details.

### 🔄 Updating (and migrating from `entire-sem`)

This plugin was renamed from `entire-sem` to `entire-graph` after `v0.1.0` — both the binary and the `cmd/` path changed. To update, or to switch over from the old `entire-sem`:

```sh
go install github.com/entireio/entire-graph/cmd/entire-graph@latest
entire plugin install "$(go env GOPATH)/bin/entire-graph" --force
rm -f "$(go env GOPATH)/bin/entire-sem"   # remove the old binary if you had it
```

> `…/cmd/entire-graph@latest` needs **v0.2.0 or newer** — `v0.1.0` predates the rename and only ships `cmd/entire-sem`, so `@latest` against it fails with *"module found (v0.1.0), but does not contain package …/cmd/entire-graph"*. If a fresh tag hasn't propagated to the Go module proxy yet, pin it (`@v0.2.0`) or use `@main`. Graph and query behavior is unchanged — only the name.

---

## 🔌 MCP & Entire Brain integration

**entire-graph is not itself an MCP server, and it exposes no MCP tools.** It is a local CLI plugin and a machine-readable **semantic provider**. There is nothing to configure as an MCP endpoint here, and no daemon to run.

MCP access to code intelligence comes from **Entire Brain** (`entire-brain`), a **separate** downstream product. Entire Brain ingests entire-graph's NDJSON output (`snapshot` / `symbols` / `edges`) into its own persistent store and is what exposes MCP tools to agents (`brain_search`, `brain_code`, `brain_impact`, `brain_context`, and so on). So the split is:

| Layer | Role | How you use it |
|---|---|---|
| **entire-graph** (this repo) | Deterministic graph engine + NDJSON provider | `entire graph <command>` on the CLI (see [AGENTS.md](AGENTS.md)) |
| **Entire Brain** (separate) | Persistence, indexing, query, **MCP server** | MCP tools in your agent, backed by the graph above |

If you want MCP-style querying inside an agent, install and configure **Entire Brain** (its own docs) — entire-graph is the graph it builds on. If you just want direct, no-egress graph queries from an agent or the terminal, call `entire graph ...` directly; no MCP layer is required.

---

## ♻️ Refresh & keeping the graph current

entire-graph has **no watch mode, no daemon, and no file-watcher**, and it needs none. The graph is **deterministic and content-addressed by `(repo, commit, tree)`**, so "refreshing" is just re-running a query — the result reflects whatever state you point it at.

There are two modes, and neither can go stale on you:

- **Working tree (default).** Every `search`, `neighbors`, and `snapshot` re-reads your live files, so results always include your uncommitted edits. Nothing to refresh — just run the command again.
- **Committed tree (`--head`).** Results are cached by the git **tree hash** (under `ENTIRE_PLUGIN_DATA_DIR`, or an explicit `--cache-dir`). Make a new commit and the tree hash changes, so the cache **automatically misses and rebuilds** — you can never read a committed-tree answer that is stale for that commit. `--no-cache` disables the cache entirely.

To warm the cache for a large repo before a latency-sensitive session, prebuild it once:

```sh
entire graph index --repo . --head --profile full --cache-dir /path/to/cache
```

Re-running `index` **is** the refresh: same tree → instant cache hit; changed tree → a fresh build. After that, `search` and `neighbors` on the same committed tree report the cache hit directly. This is the whole "keep it current" story — deterministic re-index, no background process.

---

## ⚡ Quick Start

```sh
# What can this graph do here?
entire graph capabilities --json          # languages (semantic vs inventory-only) + relation types
entire graph doctor --json                # environment, repo resolution, no_egress=true

# Find the code for a task (ranked, with source and file:line)
entire graph search --repo . --query "retry logic for webhook delivery" --format text --top-k 8

# See what changed in the last commit, at the entity level
entire graph commit HEAD

# Stream the whole graph for another tool to ingest
entire graph snapshot --repo . --format ndjson
```

Running outside an Entire session? Point the plugin at a repo with `--repo .` (or `ENTIRE_REPO_ROOT=/path/to/repo`).

---

## 🧩 Commands

| Command | What it does |
|---|---|
| `entire graph search --query "..."` | 🔍 Ranked source regions for a natural-language task |
| `entire graph neighbors --symbol NAME` | Callers / callees / relations for one symbol (impact) |
| `entire graph symbols --format ndjson` | Full stream of symbol definitions |
| `entire graph edges --format ndjson` | Full stream of relations (all 30 types) |
| `entire graph snapshot --format ndjson` | 🕸️ Full graph: header + files + symbols + relations |
| `entire graph commit <rev>` | Entity-level semantic diff of a commit vs its first parent |
| `entire graph diff --base <a> --head <b>` | Semantic diff between two refs (`analyze` is an alias) |
| `entire graph checkpoint <id>` | Semantic diff for the commit behind an Entire checkpoint trailer |
| `entire graph index --head` | Prebuild / warm the durable committed-tree cache |
| `entire graph capabilities --json` | Languages, relation types, features, network requirements |
| `entire graph doctor --json` | Environment, repo resolution, plugin data dir, `no_egress=true` |
| `entire graph version [--json]` | Provider name and plugin version |

Full flags and the agent-facing operating guide are in **[AGENTS.md](AGENTS.md)**. Diff commands print human-readable text by default and structured output with `--json`:

```text
Semantic changes HEAD~1..HEAD

auth.py
  ~ function validate_token signature changed (14 dependents)
  + class TokenClaims added
  - function parse_token removed (0 dependents)
```

---

## 🗣️ Language support

`capabilities --json` exposes two honest tiers: `semantic_languages` (parser-backed call/type/data-flow analysis) and `inventory_only_languages` (stable file/document records, no semantic claims). Current counts: **185 recognized names, 36 semantic, 149 inventory-only.**

**36 semantic languages:** Bash, C, C#, C++, CUE, Clojure, ClojureScript, Dart, Elixir, Erlang, F#, Go, Groovy, HCL/Terraform, Haskell, Java, JavaScript, Julia, Kotlin, Lua, OCaml, Objective-C, PHP, Perl, Protocol Buffers, Python, R, Ruby, Rust, SQL, Scala, Swift, TypeScript, YAML (including GitHub Actions workflow sections and jobs), Zig, Zsh.

Everything else is reported as an honest partial failure rather than dropped silently. See [docs/language-support.md](docs/language-support.md) for the full two-tier matrix.

---

## 📊 Benchmarks

All figures below are measured on real, public repositories, not estimated. Reproduce them with `cmd/graph-bench` (see [docs/benchmarks.md](docs/benchmarks.md)).

### 🎯 Accuracy vs codebase-memory-mcp

Fixed-answer semantic tasks (definition lookup, call graph, imports, change-impact) on real checkouts, scored as correctness counts.

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

---

## ⚡ Performance

- **Semantic diff:** about 0.1s for a typical `HEAD~1..HEAD` on redis.
- **Full-graph build:** linear in repository size; 23K relations in 1.5s up to 2.27M relations in 25.6s across the repos above.
- **Streaming output:** `snapshot` emits records as it parses, so memory stays bounded on very large repositories.
- **Cached committed-tree search:** reuses a tree-keyed compressed index across invocations when `ENTIRE_PLUGIN_DATA_DIR` is set, so repeated queries on an unchanged tree skip re-parsing.
- **Explicit preindex:** `index --head` builds and verifies that query-independent artifact before latency-sensitive work; cached `search` and `neighbors` calls then report the hit directly.

Absolute numbers are environment-sensitive (measured on Apple Silicon). Read them as relative signals and reproduce locally with the harness.

---

## 🔒 Security & local-first

- **Zero egress.** No API keys, no network calls, no telemetry, no grammar downloads at runtime. All 36 grammars are vendored and compiled in.
- **Verifiable.** `entire graph doctor --json` reports `no_egress=true`. Pass `--no-network` to make the no-egress contract explicit to callers.
- **Writes only to Entire's managed plugin directory.** Your code is read locally and never leaves the machine — safe to point at private repositories.
- **Reads committed `HEAD` by default** for provider/graph streams; pass `--worktree` to include live edits.

---

## 🏗️ Architecture

```text
cmd/
  entire-graph/     Plugin entry point (semantic diff, search, provider commands)
  graph-bench/      Reproducible benchmark driver
internal/
  cli/              Hand-rolled command dispatch and flag parsing
  sem/              Tree-sitter parsing, symbol + relation extraction, rename reconciliation, search
  gitutil/          Git subprocess wrappers (NUL-delimited output parsing)
  bench/            Measurement core (throughput, memory, coverage)
docs/               Language support matrix, benchmarks, operations, provider requirements, ADRs
scripts/            Local install and checksum-backed release archives
```

The parser is isolated behind `internal/sem`, so the command surface stays stable while the semantic model gets richer. The wire schema is a frozen `1.x` GA contract (additive-only minors; see `docs/adr/0001-ga-schema-contract.md`).

---

## ⚠️ Current limits

- Dependent counts are heuristic, not compiler / type-checker accurate.
- `CALLS`, `HANDLES_ROUTE`, and `HANDLES_TOOL` relations are heuristic.
- Rename detection is heuristic.
- Unsupported languages are reported as partial failures, not parsed semantically.
- The plugin is invoked as `entire graph ...`; it requires no changes to the main Entire CLI.

---

## 📄 License

MIT. The runtime parser dependency is `github.com/smacker/go-tree-sitter` (MIT). Three tree-sitter grammars are vendored as source under their own upstream MIT licenses: Dart (`internal/sem/grammars/dart/`), PostgreSQL (`internal/sem/pgsql/`), and Zsh (`internal/sem/zsh/`). This implementation does not copy or vendor Ataraxy Labs code.
