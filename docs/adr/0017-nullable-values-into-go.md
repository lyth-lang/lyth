# ADR 0017: Nullable values may be passed to Go APIs

**Status:** Accepted (refines ADR 0009)

## Context
Go parameters of nil-able types are platform types in Lyth (`Any!`,
`*User!`), because Go does not declare whether nil is accepted. ADR 0009
allowed platform values to be *used* as non-null, but the checker rejected
passing a nullable Lyth value *to* a platform parameter. In practice that
blocked the most common database and JSON idiom: optional values are `*T`
(`String?` is `*string`), and drivers read a nil pointer as SQL `NULL`:

```kotlin
pool.Query(ctx, "UPDATE todos SET title = COALESCE($1, title)", patch.Title)
```

## Decision
A value of type `T?` may be passed where a platform type `U!` is expected if
`T` is assignable to `U` and the Go representation of `T?` is assignable to
`U` in Go. The value goes in its Go representation, exactly as Go code would
pass it: `*T` for an optional value type, the nil-able value itself otherwise.
Lyth-declared non-null types (`Any`, `*User`) still reject nullable values.

## Alternatives
- **Map null to a nil interface and unbox non-null values** (`Int?` to `any`
  becomes `nil` or an `int`): tidy in Lyth terms, but every such argument needs
  generated statements, and Go APIs that expect `*T` (pgx, `database/sql`,
  `encoding/json`) would receive a different type than Go code passes.
- **Keep rejecting**: forces `!!` or manual unboxing at every optional query
  parameter, which is unsafe or noisy for the most common case.

## Consequences
Kotlin's rule for Java platform types holds: Lyth cannot check what Go does
with nil, so it does not pretend to. `fmt.Printf("%v", x)` for `x: Int?`
prints a pointer address, as it would in Go. `println` and string templates
are Lyth constructs and print the value or `null`. The planned mapping of
`T?` to Lyth-declared interfaces (roadmap Phase 4) is unaffected.
