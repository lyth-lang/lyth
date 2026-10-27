# ADR 0009: Nullability with platform types and boxed optionals

**Status:** Accepted; smart casts of locals amended by [0031](0031-go-spelling.md) (locals that are never reassigned); optional values amended by [0049](0049-optional-values-are-copies.md)

## Context
Nil dereference is Go's most common panic. Kotlin-style null safety helps, but
every Go API returns pointers and interfaces of unknown nullability.

## Decision
Pointers, interfaces, function types, and channels are non-null by default;
`T?` is nullable (same Go type). Slices and maps are never null (nil is empty).
Go-originated values have platform nullability (usable as either, no inserted
checks). Value types get optional forms `T?` represented as `*T` and never
mutated through. Smart casts work for `val`s and parameters. `?.`, `?:`, `!!`.
The keyword is `null` (lowers to `nil`).

## Alternatives
- **No null safety (Go semantics)**: forgoes a major safety win.
- **Treat all Go pointers as nullable**: every interop call needs `!!`. Unusable.
- **Insert runtime checks at platform boundaries** (Kotlin's approach): hidden
  branches and panics. Kept as a future opt-in mode.
- **Generic `Option[T]` struct runtime type**: needs a runtime package and fights
  idiomatic Go APIs (`*T` for optional JSON/SQL values).

## Consequences
Optional values interoperate with JSON and SQL natively. Boxing costs an
allocation where escape analysis cannot avoid it (documented). `T?` on type
parameters is deferred. Platform types are a deliberate soundness gap.
