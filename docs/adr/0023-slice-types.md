# ADR 0023: Slice types are spelled `[]T`

**Status:** Accepted (supersedes the `List<T>` part of 0011); its borrowing rule and `Map<K, V>` amended by [0031](0031-go-spelling.md)

## Context
ADR 0011 spelled Go slices `List<T>` to match `Map<K, V>`. In Kotlin, `List`
is a read-only interface whose contents cannot change and which never aliases
another list's storage. A Go slice does both: `append` may or may not reuse
the backing array, two slices can share elements, and the contents of a
`val` slice can still be assigned. A Kotlin programmer reading `List<Int>`
expects the wrong semantics, and the mistakes it invites (aliasing after
`append`, mutation through a shared slice) are real bugs.

The rule Lyth uses for syntax is that a Kotlin spelling is borrowed only when
the construct behaves the way a Kotlin programmer expects; otherwise Go's
spelling is used. `Map<K, V>` passes that test (a Go map behaves like a
mutable Kotlin map), arrays were already `[N]T`, and `List<T>` fails it.

## Decision
- Slice types are written `[]T`, as in Go, including nested and composite
  forms (`[][]Int`, `[]*User`, `[]Map<String, Int>`).
- A slice type is an operand where Go allows one: conversions `[]Byte(s)` and
  `make([]Int, n, c)`. `[]` directly followed by a type on the same line is a
  slice type; otherwise it is an empty list literal (`val xs: []Int = []`).
- A `?` after the element type belongs to the element: `[]User?` is a slice
  of nullable users and `[]*User?` a slice of nullable pointers. This is the
  ambiguity ADR 0011 cited against Go's prefix syntax; it has one useful
  reading because Lyth slices are never nullable (E0406). A nullable slice is
  written `([]User)?` and rejected. Arrays follow the same rule (`[4]Int?`).
  Elsewhere `?` still applies to the whole type it follows (`*User?` is a
  nullable pointer).
- `List<T>` is E0008 with a hint (`write []Int`). Diagnostics print slice
  types as `[]T`.
- List literals keep their spelling: `[1, 2, 3]`, `[]`.

## Alternatives
- **Keep `List<T>`**: consistent with `Map<K, V>`, but a false friend.
- **`Slice<T>`**: honest about the semantics, but a third spelling of a type
  every Go signature, document, and tool prints as `[]T`.
- **`MutableList<T>`**: still promises Kotlin's copy-free, non-aliasing model.

## Consequences
Signatures read the same in Lyth and in `go doc`. The `?` rule differs between
`[]T?` (element) and `*T?` (whole type), which the spec states next to the
nullability rules. Types and literals now differ
in form (`[]Int` versus `[1, 2]`), which matches Go. `Map<K, V>` stays, so
collection types are not uniform; that is accepted because map semantics match
the Kotlin spelling.
