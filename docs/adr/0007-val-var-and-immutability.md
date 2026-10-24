# ADR 0007: `val`/`var` and value immutability

**Status:** Superseded for local variables by [0031](0031-go-spelling.md) (`:=` declares mutable locals); parameters, loop variables, and value receivers stay immutable

## Context
Go has no immutable local variables. Kotlin's `val` freezes only the reference,
but Lyth structs are values, so the meaning must be defined for value types.

## Decision
`val` (and parameters, loop variables, value receivers) are immutable: the
value stored in the binding never changes, including fields and array elements
reachable without a pointer (Swift `let` semantics). Mutation through pointers,
slices, maps, channels, and interfaces is unaffected. `&x` and pointer-receiver
calls require mutable storage. No `:=`; it gets a targeted diagnostic.

## Alternatives
- **Kotlin semantics (reference-only)**: meaningless for value types, since
  `u.name = x` on a `val` struct would mutate the binding.
- **Deep immutability**: unenforceable across Go code and incompatible with Go
  APIs.
- **`let`/`var` (Swift)**: visually more distinct, but `val`/`var` is the
  stated preference and familiar to Kotlin users.
- **Keeping `:=`**: a second declaration syntax that Go developers read as
  mutable.

## Consequences
Compile-time only, with zero runtime cost. Catches Go's silent value-receiver
write bug. Requires `var` for Go idioms that mutate locals (`json.Unmarshal(&v)`,
`var mu: sync.Mutex`). Smart casts become sound for `val`s.
