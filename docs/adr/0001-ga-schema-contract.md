# ADR 0001 — GA semantic-provider schema contract

Status: Accepted
Date: 2026-07-03

## Context

`entire-sem` emits a semantic index consumed by downstream tools (notably
`entire-brain`). The wire format carries `schema_version` in `major.minor` form.
The provider currently advertises **`1.1`** (`internal/sem/provider.go`
`SchemaVersion`), where the `1.1` minor adds *optional, additive* relation fields
that tolerant readers ignore. A compatibility policy already exists in
`docs/semantic_provider_requirements.md`, but it was never ratified as the frozen
contract for General Availability, and the requirements doc's example header still
showed the older `1.0`.

For GA we need a single, stable, machine-checkable contract so consumers can pin
against it and so future changes have clear, non-breaking rules.

## Decision

**GA ships on schema `1.x`, with `1.1` as the current minor. `1.x` is the frozen,
stable GA contract.** We do NOT roll back to `1.0`; `1.1` is strictly additive
over `1.0` and every `1.0` reader already tolerates it.

The contract, stable for the entire `1.x` major:

1. **Major = compatibility boundary.** Consumers refuse an unknown *major*
   version. Everything within `1.x` is guaranteed mutually intelligible.
2. **Minors are additive only.** A new minor may add optional fields or optional
   record kinds; it may never remove a field, change a field's meaning, or make a
   previously-optional field required.
3. **Tolerant readers required.** Consumers ignore unknown fields within a
   supported major, and warn (not fail) when they see a newer supported-major
   minor, since additive facts may have been skipped.
4. **Extensions are namespaced.** Unknown/experimental relation types use an
   `X-provider:RELATION` namespace so they never collide with core types.
5. **Breaking changes require a major bump** (`2.0`) and a migration note; they are
   out of scope for the `1.x` GA line.

`entire-brain` ingestion MUST follow the tolerant-reader rules above: accept any
`1.x`, ignore unknown fields, warn on a newer minor.

## Consequences

- Consumers may pin `>=1.0 <2.0` and rely on additive-only evolution.
- The `1.1` additive relation fields are part of GA; they are not gated or
  experimental.
- A follow-up adds a brain-side ingestion contract test that asserts
  `entire-brain` parses current `entire-sem` `1.x` output (tracked separately).
- The stale `1.0` example header in `semantic_provider_requirements.md` is updated
  to `1.1` for consistency with the emitted version.
