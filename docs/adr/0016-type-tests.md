# ADR 0016: Type tests are type assertions, with smart casts

**Status:** Accepted; smart casts of locals amended by [0031](0031-go-spelling.md)

## Context
Go code inspects dynamic types with type assertions (`v, ok := x.(T)`) and
type switches. Kotlin has `is`, `!is`, `as`, `as?`, and smart casts. Lyth needs
the capability (optional interfaces such as `if (store is pinger)`, decoding
`Any` values, error types) without hiding what the Go code does.

## Decision
- `x is T`, `x !is T`, `x as T`, and `x as? T` require an interface-typed
  operand and a non-null `T`. They lower to type assertions: `as` panics like
  `x.(T)`, and `as?` yields null (`t, _ := x.(T)`, boxed for value types).
  Impossible tests (a concrete type that does not implement the interface)
  are compile errors, as in Go.
- A successful test smart-casts an immutable variable, like a null check. The
  generated code binds the asserted value once, in Go's idiom:
  `if nf, ok := err.(*NotFound); ok`, `switch s := s.(type)`, and
  `nf, ok := err.(*NotFound); if !ok { return }`. A `when` whose conditions are
  all `is` tests is a type switch.
- Type tests see the dynamic type only. They do not unwrap wrapped errors; for
  errors, use `errors.AsType<T>(err)` (Go 1.26) or `errors.Is`.

## Alternatives
- **`is` unwraps errors (`errors.As` semantics)**: convenient for errors but
  surprising for everything else, and a hidden behavior difference from Go.
- **Smart casts as repeated assertions (`x.(T)` at each use)**: simpler to
  generate, but not what Go programmers write; bindings cost nothing and read
  naturally.
- **No `as`**: `as?` plus `!!` covers it, but `as` matches the cost and
  behavior of `x.(T)` exactly.

## Consequences
Generated code is idiomatic and allocation-free (assertions on interfaces).
Mixed `when` conditions (`is` together with values, or conditions that need
statements) lower to `if`/`else` chains so each condition is evaluated only
when reached. Binding names come from the tested type (`circle`, `nf`) and
avoid every user and package-level name in the function.
