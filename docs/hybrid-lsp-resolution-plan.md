# Plan: Hybrid LSP call resolution for entire-sem (a parallel, opt-in path)

## Goal & constraint

Add an **optional, parallel** LSP-backed call-resolution path that augments — does
not replace — the existing tree-sitter heuristic resolver. The fast, no-toolchain,
portable heuristic path stays the **default**; the LSP path is a higher-fidelity
**tier** you opt into when you have the language's server and can pay its cost.

This is the deliberate counterpart to the "#2 / real type resolution" tradeoff we
kept deferring: rather than rebuild a type system inside entire-sem, drive the
language's own analyzer (the same move CBM's "Hybrid LSP" makes), but keep it as
an add-on so entire-sem's identity — fast and LSP-free by default — is preserved.

## Why (grounded in the brain-bench results)

The four-language eval pinpoints exactly where the heuristic resolver hits a
ceiling, and it's always the same thing — calls that need real type/macro
resolution:

| language | call-graph F1 (entire-sem / CBM) | the gap is… |
|---|---|---|
| Go | 0.79 / 0.62 | (entire-sem ahead — LSP would mostly raise the exact tier) |
| TypeScript | 0.79 / 0.71 | typed-receiver `obj.method()` |
| Python | 0.82 / 0.64 | `var.method()` receiver type inference |
| Rust | 0.50 / 0.69 | **macro-hidden calls + generic/trait dispatch** |

Rust is the clearest case: tree-sitter can't expand macros (`next!`/`expect!`) or
resolve generic trait dispatch; rust-analyzer does both. An LSP path closes that
cell, and lifts the `change-impact` task everywhere (it's recall-bound on the same
type-dependent receiver calls).

## Design principles

1. **Additive & opt-in.** A new resolution mode; the default profile is untouched.
2. **Heuristic stays the backbone.** Tree-sitter remains the source of symbols,
   structural relations (DEFINES/CONTAINS/IMPORTS/EXTENDS/…), and the cheap call
   edges. The LSP augments only *call resolution* — the hard part.
3. **Per-language, gracefully degrading.** An LSP server registry; if a language's
   server is absent/unconfigured, that language silently falls back to heuristic.
4. **Provenance + confidence.** Every CALLS edge is tagged with its resolution
   source (`heuristic` vs `lsp`); LSP edges are the `exact` tier (mirrors the
   existing exact/`lsp_direct` notion and CBM's tiering).
5. **Merge, never silently drop.** Final edges = heuristic ∪ LSP, deduped by
   `(from,to)`; on conflict the LSP/higher-confidence edge wins. (An `lsp-only`
   call mode is also useful for clean A/B measurement.)

## Architecture

### A generic LSP client — `internal/sem/lsp`
JSON-RPC over stdio: `Content-Length` framing, request/response correlation,
**auto-ack of server→client requests** (`client/registerCapability`,
`window/workDoneProgress/create`) so it can't deadlock, and **index-readiness
tracking** via `$/progress`. Reference implementation already exists and is
proven: brain-bench's `oracle-rust/oracle.py` is exactly this client for
rust-analyzer's call hierarchy — port its logic to Go (it encodes the two hard-won
lessons: wait for `cachePriming` end, not the earlier `Roots Scanned`; bound every
request and skip-on-stall so one wedged symbol can't sink the run).

### An LSP call resolver
Per repo, one server session. For each tree-sitter function/method symbol, use its
`selectionRange` (name) position to run `textDocument/prepareCallHierarchy` +
`callHierarchy/outgoingCalls`. Map each `CallHierarchyItem.selectionRange.start`
back to entire-sem's `(file, name-line)` identity — the same mapping the
oracle-rust client uses — and keep only in-project callable targets (drop
std/external), matching the existing call scope.

### Server registry (`language → LSP`)
Parallel to the existing tree-sitter grammar registry:

| language | server | availability check |
|---|---|---|
| Rust | `rust-analyzer` | rustup component / PATH |
| Go | `gopls` | PATH |
| TypeScript/JS | `typescript-language-server --stdio` (tsserver) | PATH/npm |
| Python | `pyright-langserver --stdio` (or `pylsp`) | PATH/pip |

Each entry: command, args, availability probe, and the project-staging step it
needs (below).

### Integration point
Add `callResolution: "hybrid"` (and `"lsp"`) to `profileSpec` and a `ProfileHybrid`
in `resolveProfile`. In `forEachRelation`'s call scan, when the mode is hybrid/lsp
and a server exists for the file's language, run the LSP resolver and merge its
CALLS edges (tagged `resolution:"lsp_exact"`) with the heuristic edges. Everything
else (symbols, structural relations, the default `full`/`fast`/`syntax-only`
profiles) is unchanged. The snapshot header records the resolution mode + server
versions (provenance/reproducibility, like brain-bench's tool stamp).

## Operational concerns (learned the hard way building the oracles)

- **Latency is real and variable.** rust-analyzer indexing took seconds on
  byteorder but **wedged past timeout on semver** (its optional `serde` dep). So
  the LSP path is opt-in only; one reused session per repo; index-readiness wait
  with a hard cap; per-request timeouts with skip-on-stall.
- **Build/deps precondition.** LSP servers need the project to resolve/build
  (`cargo metadata`, `npm install`, a Python env). Reuse a brain-bench-style
  `install_deps` staging step; if it fails, fall back to heuristic for that repo.
- **Caching.** Key LSP results by file content (entire-sem already has `BodyHash`)
  so unchanged files aren't re-queried across runs — essential to make repeat runs
  affordable.
- **Determinism.** Pin server versions; record them in the header.

## Independent ground truth (the precondition that makes the eval clean)

brain-bench's premise is a **tool-independent oracle: the compiler**. As long as
the oracle is the language's *authoritative batch compiler analysis* and the
tools (entire-sem heuristic, CBM, the new hybrid) are separate, the comparison is
sound — including for the hybrid path. Three of four oracles already satisfy this;
one doesn't, and fixing it is part of this plan (Phase 0).

**The actual problem is the Rust oracle, and it's a pre-existing bug, not an LSP
issue.** The Rust oracle is currently *rust-analyzer* — an IDE engine, not the
compiler. That already violates "tool-independent oracle" (any rust-analyzer-based
tool, including CBM's LSP, scores against itself), and it would make a
rust-analyzer hybrid trivially circular. **Fix: make the Rust oracle compiler-grade
— driven by rustc, reading the resolved call graph from MIR.** MIR is post-macro-
expansion and post-monomorphization, so it is both *independent of every IDE
engine* and *strictly more complete* (it captures exactly the macro-hidden and
generic/trait-dispatch calls that rust-analyzer and entire-sem approximate). That
single change turns Rust from the circular case into the cleanest one.

**The other three are already compiler ground truth, distinct from their IDE
LSPs:**
- **Python** — oracle `jedi`, LSP `pyright`: different engines entirely. Fully
  independent.
- **Go** — oracle is `go/types` resolution computed in `cmd/oracle`; the LSP is
  `gopls`. Same type checker, but the oracle is the compiler's authoritative
  resolution and gopls is a separate tool that re-derives a call graph and *can
  diverge* — that divergence is exactly what we measure, not a tautology.
- **TypeScript** — oracle is the tsc compiler API (`getResolvedSignature`), LSP is
  `tsserver`'s call-hierarchy provider: the same type checker via two different
  code paths that demonstrably disagree on edge cases. Not self-referential.

So after Phase 0 the rule is simply: **the oracle is the compiler; every tool
(heuristic, CBM, hybrid) is measured against it, none of them IS it.** No caveat.

## Validation — brain-bench is the harness

With independent oracles in place, add an `entire-sem-hybrid` system alongside
`entire-sem` / `cbm` / `naive` and measure across all four languages directly.
Expected: hybrid (a) closes the Rust call-graph cell against the *MIR* oracle
(genuine, since MIR ≠ rust-analyzer), (b) lifts TS/Python `change-impact`, (c)
reaches/matches CBM (itself a hybrid-LSP tool, now also measured against
independent ground truth). The existing cost block quantifies the
**accuracy-vs-latency tradeoff** — the whole reason the heuristic path stays the
default. Secondary metric: **coverage of the heuristic's miss set** (what fraction
of the edges the heuristic misses does the LSP recover), which isolates the LSP's
contribution regardless of absolute F1.

## Phasing

0. **Compiler-grade, independent Rust oracle (brain-bench).** Replace the
   rust-analyzer Rust oracle with a rustc/MIR-driven one (resolved call graph from
   MIR: post-macro, post-monomorphization). Rebaseline `results-rust`. This alone
   fixes the benchmark's tool-independence for Rust — *before* any hybrid work, so
   the later comparison is clean. (Bonus: it likely *raises* the truth-edge count,
   re-grounding all the Rust numbers, including the existing entire-sem/CBM ones.)
1. **Spike (Rust hybrid).** Go LSP client (port of brain-bench's proven
   rust-analyzer call-hierarchy client) + resolver; wire `ProfileHybrid` for Rust
   only. Measure against the new MIR oracle — a genuine comparison (MIR ≠
   rust-analyzer).
2. **Generalize.** Server registry + gopls / tsserver / pyright; per-language
   availability + staging.
3. **Merge, provenance, profile, header stamp.** `hybrid` + `lsp-only` modes;
   edge `resolution` source; server versions in the snapshot header.
4. **Caching + perf hardening.** Content-hash cache; session reuse; bounded
   readiness/requests.
5. **brain-bench `entire-sem-hybrid` system.** Full four-language measurement +
   the accuracy/latency tradeoff writeup, all against independent oracles.

## Non-goals
- Replacing or weakening the heuristic path (it stays the default and the fallback).
- Making entire-sem depend on any LSP to function.
- Building a type system inside entire-sem (the LSP *is* the type system, on demand).
