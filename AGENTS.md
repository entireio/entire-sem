# AGENTS.md — operating guide for coding agents

Hand this to any coding agent working in a repo where the `entire graph` plugin is installed. It is the difference between the graph saving tokens and not: it moves you from grep/read exploration to graph queries, which is where most of a session's token budget goes.

## What this gives you

A precomputed, **deterministic** code graph is available through the `entire graph` command — functions, classes, methods, types, routes, and the calls/inheritance/field/service relations between them, parsed with tree-sitter, 100% locally (no network, no model, no keys). Use it to **LOCATE** and **UNDERSTAND** code *before* any grep / find / cat / whole-file read. Every command is no-egress and safe to run inside a sandboxed session. The same commit always yields the same graph, so once the graph shows you something, you can trust it and act — no need to re-confirm with a second tool.

Default flags to remember: pass `--repo .` when you're not inside an Entire session; the graph reads your **working tree by default** (your uncommitted edits are visible), and `--head` switches to committed-tree semantics with a cached, reusable index.

---

## The parts of the graph

Reach for the smallest tool that answers your question.

### 🔍 search — *find the code for a task* (your first move)
Ranked source regions for a plain-language description, with the source and `file:line` inline. Hybrid ranking over bodies, identifiers (camelCase/snake_case aware), signatures, paths, and graph neighbors. Output is budgeted (16 KiB by default) to drop straight into context.

```sh
entire graph search --repo . --query "<the task or bug in one plain sentence>" --format text --top-k 8
```

- `--format agent` for compact ranked output with latency telemetry; `json`/`ndjson` for the full schema (completeness, partial failures, diagnostics).
- `--top-k N` result count; `--max-context-bytes N` byte budget (`0` = unbounded).
- Working tree by default; add `--head` for committed-tree + cache reuse.
- `--profile syntax-only|fast|full` (default `syntax-only`); `--index-all-files` or `--max-indexed-files N` to widen/bound cold-search parsing.

**When:** the start of essentially every task. One good query lands you on the fix area.

### 🕸️ neighbors — *who calls this / what does it call* (impact, targeted)
Direct incoming/outgoing relations for **one** symbol, with definition locations, plus bounded two-hop paths at `--depth 2`. **This is the impact query** — not `edges` (which is a full stream).

```sh
entire graph neighbors --repo . --symbol NAME --relation CALLS --direction in   # who calls NAME
entire graph neighbors --repo . --symbol NAME --relation CALLS --direction out  # what NAME calls
```

- `--file path` — **required** when the symbol name is ambiguous (multiple defs); the graph returns definitions only until you disambiguate.
- `--relation CALLS` (default is the call family) — pick another relation to follow it instead.
- `--direction both|in|out`, `--depth 1|2`, `--limit N`.
- `--internal-only` drops unresolved external endpoints; `--exclude-tests` drops test-only neighbors.
- `--format agent|text|json`; `--head` for cached committed-tree; `--profile fast` for shallow call resolution (default `full` favors correctness).

**When:** "what breaks if I change X", "who uses this", tracing a call chain — after search has given you a concrete symbol name.

### 📇 symbols — *definitions*
Full stream of symbol records (stable `compound-v1` ID, kind, qualified name, source range, signature, language, container). This is a **bulk NDJSON stream of the whole repo**, filtered to the symbol record type — there is **no positional name argument** and no server-side name filter; grep the stream client-side, or prefer `search`/`neighbors` for a targeted single-symbol lookup.

```sh
entire graph symbols --repo . --format ndjson [--worktree]
```

**When:** you need the complete definition inventory (e.g. ingesting into a store), not a single lookup.

### 🔗 edges — *relations*
Full stream of relation records across all 30 types (`CALLS`, `IMPORTS`, `EXTENDS`, `HANDLES_ROUTE`, …), each tagged with resolution and confidence. Like `symbols`, this is the **whole-repo stream** — there is **no `--to`/`--from`/`--relation` filter**; for one symbol's callers/callees use `neighbors`, not `edges`.

```sh
entire graph edges --repo . --format ndjson [--worktree]
```

**When:** you want every relation (bulk export / ingestion). For a targeted question, use `neighbors`.

### 🗺️ snapshot — *the whole graph*
One header record, then file, external-endpoint, symbol, and relation records, streamed so memory stays bounded. Superset of `symbols` + `edges` + files.

```sh
entire graph snapshot --repo . --format ndjson [--worktree]
```

**When:** ingesting the full graph into agent memory or a store such as Entire Brain.

### 🧬 diff / analyze / commit / checkpoint — *what changed + risk*
Entity-level change list (added / removed / renamed / signature-changed / body-changed) with a heuristic **dependent count**, so a signature change with many dependents stands out.

```sh
entire graph commit HEAD --json                     # a commit vs its first parent
entire graph diff --base main --head HEAD --json    # between two refs (analyze is an alias)
entire graph checkpoint <id> --json                 # the commit behind an Entire-Checkpoint trailer
```

**When:** judging whether a change is safe to keep / revert / continue, or reviewing a branch/PR. High dependent counts on a signature change = run tests first.

### 🏗️ index — *build / warm the cache*
Prebuilds the durable, query-independent committed-tree index and verifies it was written, before latency-sensitive work.

```sh
entire graph index --repo . --head --profile full --cache-dir /path/to/cache --format json
```

**When:** once, up front, on a large repo before a batch of `--head` searches/neighbors queries. Re-running it is also how you "refresh" a committed-tree cache — same tree hits, changed tree rebuilds.

### 🧭 capabilities / doctor / version — *feature-detect*
```sh
entire graph capabilities --json    # semantic vs inventory-only languages, relation types, features
entire graph doctor --json          # environment, repo resolution, no_egress=true
entire graph version [--json]       # provider name + plugin version
```

**When:** before assuming a language is semantically parsed, or to confirm the no-egress environment.

---

## Operating doctrine (the token-saving rules)

1. **Search first — always.** Your first move on any task is one `entire graph search --query "<task>"`. Do **not** grep / find / cat to locate code before you have searched. Exploration is where ~90% of a session's tokens are wasted.
2. **Then narrow, only as needed.** Search exposes concrete identifiers → use at most one `neighbors --symbol X` (for impact) or read the returned line ranges. Don't fan out.
3. **Trust the graph.** Once search or neighbors shows you the function and its source, **edit it**. Do not re-read the whole file or re-grep to "confirm" what the graph already showed — the graph is deterministic.
4. **Never read a whole file to explore.** If you must read, read the line range around the symbol. To understand a type/class, query it — don't open its file.
5. **Impact = one targeted query.** For "what breaks if I change X", use `neighbors --symbol X --relation CALLS --direction in` — not a whole-graph `snapshot`/`edges` dump, and not a repo-wide grep.
6. **Minimise turns.** Token cost is roughly turns × context. Prefer one precise query over three broad ones. Stop discovery once you can defend the edit with a focused hypothesis (ideally a failing test).
7. **Feature-detect before you trust.** If a language might be inventory-only, check `capabilities --json` first — inventory-only files have file records but no semantic relations.

Quick mental model:

```text
locate  →  entire graph search --query "..."          (ranked code + file:line)
impact  →  entire graph neighbors --symbol X ...       (callers/callees of X)
change  →  entire graph diff --base A --head B          (entity-level, with dependents)
ingest  →  entire graph snapshot --format ndjson        (whole graph)
```

---

## Working on entire-graph itself

If your task is modifying this repository (not just using it), the build/test surface is in `mise.toml`:

```sh
mise run build   # go build -o entire-graph ./cmd/entire-graph  (needs CGO for tree-sitter)
mise run test    # go test ./...
mise run check   # fmt + vet + race tests + build
```

Contract rules that must not break: schema `1.x` is frozen and additive-only (`docs/adr/0001-ga-schema-contract.md`); the provider is **no-egress** (never add remote fetches, hosted API calls, telemetry, or runtime grammar downloads); `compound-v1` symbol IDs must stay stable across ordinary edits; unsupported/unparseable files must surface as machine-readable partial failures, never silent drops. All logic lives under `internal/` (`sem` = parsing/graph/search, `cli` = hand-rolled dispatch, `gitutil` = git subprocess); `cmd/entire-graph/main.go` is a thin entry point. The plugin manifest (`entire-plugin.yml`) registers the subcommand `graph`, so users type `entire graph ...`. This project was **previously named `entire-sem`** — do not reintroduce the old name. **Entire Brain** (`entire-brain`) is the separate downstream consumer of this provider's NDJSON — not an old name for this project.
