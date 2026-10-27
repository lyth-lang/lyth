# ADR 0049: Optional values are copies of a pointer's value

**Status:** Accepted (2026-10-27). Amends [0009](0009-nullability.md)
(optional values) and the read-only view of types.md §5.6. Breaking: a Lyth
`*T` or `*T?` no longer converts implicitly to `T?`.

## Context

For a value type `T`, `T?` is an optional value: a `T` or nothing, which
never changes once made. It is a `*T` in Go, as is a nullable pointer
`*T?`. So the language kept them apart by behavior: an optional value cannot
be changed through (E0502, E0504), compares by contents, and goes into `any`
as its value (ADR 0040), while a `*T?` is Go's pointer with a nil check.

One rule broke that: a `*T` or `*T?` converted implicitly to `T?`, as "a
read-only view". The view was read-only for its holder only. The pointer's
other holders could still change the `T`, so an optional value changed under
the code that held it, with no Go code involved:

```kotlin
p := &User(Name = "a")
o: User? = p
p.Name = "b"
println(o?.Name)   // printed b
```

A function that kept a `User?` argument saw its caller's later changes the
same way, and types.md said that sharing the box was "unobservable from
Lyth". Teaching made it worse: the guides said "`User?` is `*User`", which is
true of the Go type and wrong about the behavior, so Go programmers expected
to change a `User` through it, and Kotlin and TypeScript programmers, for
whom `User?` is a nullable reference, did too.

Optional values earn their place in JSON and SQL code: the repository's own
code declares `string?`, `time.Time?`, `bool?`, and `int64?`, and a struct
(`time.Time`) is among them, so the line between the two concepts cannot be
"struct or not".

## Decision

- **No implicit conversion from a Lyth pointer.** A `*T` or `*T?` becomes a
  `T?` only as a copy written in the source: `*p` for a `*T` (`users[i]` for
  `&users[i]`), and `if p != null { *p } else { null }` for a `*T?`. The
  copy is visible, and so is its cost (a box, `new(*p)` in Go).
- **E0201 explains it.** The note says that the optional value never changes
  and the pointer's target may; the hint gives the copy, as an edit for a
  `*T`. It is a hint, not a fix: a copy no longer follows later changes,
  which is the point, but it is a change of behavior.
- **Go pointers still convert.** A Go `*T!` (a `*string` field of a Go
  struct, a `*time.Time` result) still reads as `T?`. Go code can change
  what it points to anyway, as with any platform value (types.md §5.4), and
  a copy at every read of a Go optional field would be noise.
- **Teach by behavior.** The guides describe `User?` as an optional value
  that never changes, and `*User?` as the pointer whose changes every holder
  sees; the Go type comes after.

## Alternatives

- **Keep the view and fix the docs.** Nothing breaks, but an "immutable"
  value that changes is a contradiction the type system would carry forever.
- **Copy implicitly.** Sound, but it hides an allocation at an assignment,
  against the rule that anything with a cost is visible in the source.
- **`T?` on a struct means a nullable pointer.** One concept for structs, but
  `time.Time?` would compare by address and allow changes through it, and the
  line between value and pointer would depend on whether a type is a struct.
- **No `T?` on structs; write `*T?`.** The same loss for `time.Time?`, the
  most useful struct optional, and every `User?` changes meaning, so no
  `lyth fix` could migrate it.

## Consequences

- An optional value never changes once made, as the spec says.
- Code that returned `&users[i]` as a `User?` writes `users[i]`; one test
  program in the repository passed `&four` as an `int?` and now passes
  `four`.
- The guide's "Changes between versions" table has a row for it.

### Implementation
- **Checker.** `assignable` in `internal/check/assign.go` keeps the view for
  platform pointers only; `pointerToOptional` adds the note and the hint.
- **Tests.** `check/optionalcopy_neg` (the hints, a Go pointer converting),
  `run/optionalcopy` (the copy keeps its value when the pointer's target
  changes).
