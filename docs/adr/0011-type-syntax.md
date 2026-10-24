# ADR 0011: Type syntax: generics-style collections, loose `?`

**Status:** Accepted; `List<T>` superseded by [0023](0023-slice-types.md); `Map<K, V>`, `Chan<T>`, `<T>`, and PascalCase primitive names superseded by [0031](0031-go-spelling.md)

## Context
Postfix `?` on Go's prefix type syntax is ambiguous (`[]*User?`), and Kotlin-like
generic syntax was requested.

## Decision
`List<T>`, `Map<K, V>`, `Chan<T>` for slices, maps, and channels. `[N]T` for
arrays, `*T` for pointers, `(A) -> R [throws]` for functions. `?` applies to the
whole type it follows (`*User?` = nullable pointer). Type arguments are explicit
scopes (`List<*User?>`). Primitive names are PascalCase (`Int64`, `String`,
`Error`, `Any`).

## Alternatives
- **Go type syntax (`[]T`, `map[K]V`)**: familiar to Go users, but forces
  precedence rules for `?` that make the common case awkward.
- **`T[]` (TypeScript)**: same ambiguity, and no map counterpart.
- **`Slice<T>`**: accurate, but unfamiliar. `List` is what most developers
  reach for. The docs state plainly that it is a Go slice.

## Consequences
Unambiguous nullability in nested types. `<` `>` in expression position need
speculative parsing for explicit type arguments (rare, planned). Go
programmers learn a small rename table (language.md §8.1).
