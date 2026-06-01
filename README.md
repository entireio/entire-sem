# Entire Sem

`entire-sem` is an Entire CLI plugin for entity-level checkpoint context.

Entire already knows a checkpoint touched `auth.py`. This plugin answers the next question:
which functions, classes, types, or methods changed inside that file?

This plugin builds a binary named `entire-sem`, which is invoked through Entire as:

```sh
entire sem commit HEAD
entire sem checkpoint abc123def456
entire sem diff --base HEAD~1 --head HEAD
entire sem analyze --json
entire sem capabilities --json
entire sem snapshot --repo . --format ndjson
entire sem symbols --repo . --format ndjson
entire sem edges --repo . --format ndjson
```

## Status

This plugin implements the semantic checkpoint context proposed in
[entireio/cli#589](https://github.com/entireio/cli/issues/589). It intentionally
does not vendor or copy Ataraxy Labs' `inspect` / `sem` projects.

The plugin uses a tree-sitter-backed parser for:

- Go
- Python
- JavaScript / TypeScript
- Rust

The parser is isolated behind `internal/sem`, so the command surface can stay stable
while the semantic model gets richer.

This branch also adds a local-only semantic provider contract for tools that need
machine-readable repository snapshots. Provider commands emit JSON or NDJSON records
for capabilities, files, symbols, and relations without fetching remote code, calling
hosted model APIs, uploading telemetry, or downloading grammars at runtime.

## Install

Requirements:

- Entire CLI
- Git
- Go toolchain with CGO support (tree-sitter uses native parser bindings)

Install the plugin binary with Go, then copy it into Entire's managed plugin
directory:

```sh
go install github.com/suhaanthayyil/entire-sem/cmd/entire-sem@latest
entire plugin install "$(go env GOPATH)/bin/entire-sem" --force
entire sem version
```

If `$(go env GOPATH)/bin` is already on your `PATH`, Entire can also discover
the binary directly after `go install`.

Entire plugins are currently local executables, not a hosted plugin marketplace:
`entire sem` works because Entire discovers an `entire-sem` binary from its
managed plugin directory or from `$PATH`.

## Install From Source

```sh
git clone https://github.com/suhaanthayyil/entire-sem.git
cd entire-sem
mise run build
entire plugin install ./entire-sem --force
```

After either install path, `entire sem ...` works anywhere the Entire CLI can
find the managed plugin.

## Commands

Compare one commit against its first parent:

```sh
entire sem commit HEAD
```

Compare two arbitrary refs:

```sh
entire sem diff --base main --head HEAD
```

Emit JSON:

```sh
entire sem diff --base main --head HEAD --json
```

Analyze the commit associated with an Entire checkpoint trailer:

```sh
entire sem checkpoint abc123def456
```

Inspect the provider and its environment:

```sh
entire sem version --json
entire sem doctor --json
entire sem capabilities --json
```

Emit semantic provider records:

```sh
entire sem snapshot --repo . --format ndjson
entire sem symbols --repo . --format ndjson
entire sem edges --repo . --format ndjson
```

`snapshot` writes a complete NDJSON stream: one header record followed by file,
symbol, and relation records. `symbols` and `edges` are filtered views over the
same snapshot builder. By default, snapshots read the committed `HEAD` tree when
git metadata is available, so dirty tracked edits and untracked files are excluded.
Use `--worktree` to include live working-tree contents, and `--no-network` to make
the provider's no-egress contract explicit to callers.

Run without installing through Entire:

```sh
ENTIRE_REPO_ROOT=/path/to/repo ./entire-sem diff --base HEAD~1 --head HEAD
```

## Provider Contract

The provider contract is designed for Entire or other local tools that want a stable
semantic graph rather than human-oriented diff text.

- `version --json` returns the provider name and plugin version.
- `doctor --json` reports Entire environment variables, repository resolution,
  plugin data directory writability, and `no_egress=true`.
- `capabilities --json` reports supported extensions, languages, parser metadata,
  relation types, optional local-only features, and network requirements.
- `snapshot --format ndjson` emits header, file, symbol, and relation records.
- `symbols --format ndjson` emits only symbol records.
- `edges --format ndjson` emits only relation records.

Snapshot headers include the schema version, provider version, repository key,
`HEAD` commit and tree when available, parsed languages, capability labels,
warnings, partial failures, and aggregate completeness stats. Symbol records include
stable compound IDs, `stable_id_version`, kind, qualified name, source range,
signature, body hash, language, and container ID when the symbol is nested.

Current relation records are:

- `DEFINES`
- `CONTAINS`
- `IMPORTS`
- `CALLS`
- `HANDLES_ROUTE`
- `HANDLES_TOOL`

Unsupported but detected source files are reported as machine-readable partial
failures instead of disappearing silently. Repositories without a readable `HEAD`
fall back to the working tree and include a warning in the snapshot header.

## Example Output

```text
Semantic changes HEAD~1..HEAD

auth.py
  ~ function validate_token signature changed (14 dependents)
  + class TokenClaims added
  - function parse_token removed (0 dependents)
```

## Use Case

When an agent changes a repo, you often need to decide if the checkpoint is safe
to keep, revert, or continue from. File names are not enough. `entire-sem` shows
the actual functions, classes, signatures, and renames that changed, so you can
understand the checkpoint before reading the full diff.

## Why This Exists

Issue [entireio/cli#589](https://github.com/entireio/cli/issues/589) proposes showing
checkpoint context at the entity level instead of stopping at "this file changed."
`entire-sem` is a plugin-shaped implementation of that idea:

- parse the before and after git trees with tree-sitter
- extract named entities like functions, classes, methods, structs, traits, and types
- compare signatures and normalized bodies
- build a heuristic dependent count from parsed references in the target tree
- report added, removed, renamed, signature-changed, and body-changed entities
- expose the same parser through local-only provider commands that emit
  JSON/NDJSON snapshot, symbol, and relation records

The implementation does not copy or vendor Ataraxy Labs code. The parser dependency is
`github.com/smacker/go-tree-sitter`, which is MIT-licensed.

## Current Limits

- Dependent counts are heuristic, not compiler/type-checker accurate.
- `CALLS`, `HANDLES_ROUTE`, and `HANDLES_TOOL` relations are heuristic.
- Rename detection is heuristic.
- Unsupported languages are reported as partial failures, not parsed semantically.
- The plugin is invoked as `entire sem ...`; it does not require changes to the
  main Entire CLI repository.
