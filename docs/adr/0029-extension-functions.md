# ADR 0029: Extension functions are Go functions called like methods

**Status:** Accepted; respelled `func []T.Map[T, R]`, with the receiver `self`, by [0031](0031-go-spelling.md)

## Context
Go only allows methods on types declared in the same package, and has no
generic methods. Code therefore calls helpers as functions, and chains read
inside out: `coll.Map(coll.Filter(todos, isOpen), title)`. Kotlin solves this
with extension functions, which are static functions with a receiver and add
no runtime machinery.

## Decision
- `fun Recv.Name(...)` with a receiver type that is not a struct or enum of
  the same package declares an extension function: on `String`, `[]T`,
  `Map<K, V>`, a type of another package, or an interface. It lowers to a Go
  function taking the receiver as its first parameter; a call `x.Name(a)` is
  the Go call `pkg.Name(x, a)`.
- Members win over extensions. An extension is in scope in its package and,
  when exported, wherever its package is imported. Two applicable extensions
  from different packages are ambiguous.
- Matching follows method rules: a pointer receiver applies to an addressable
  value, a value receiver to a pointer, an interface receiver to its
  implementations, and an untyped constant by its default type. Type
  parameters are inferred from the receiver, then from the arguments.
- One Go function per name: a package declares at most one extension of a
  name, and no other top-level declaration may use it.
- The body reaches the receiver through `this`; members are not in scope
  unqualified, because the receiver's type belongs to someone else.
- Extensions have no method values, do not apply to nullable receivers or
  through `?.` yet, and never satisfy interfaces.

## Alternatives
- **Uniform function call syntax** (any function callable as a method on its
  first argument): makes every function a candidate for every value and
  turns name lookup into overload resolution.
- **Implicit member scope inside extensions**: shorter bodies, but blurs
  which names belong to a foreign type, including its unexported ones.
- **Several extensions of one name for different receivers**: natural in
  Kotlin, but needs mangled Go names, which Go callers would see.

## Consequences
Library functions read like methods without any runtime cost, and Go callers
see ordinary functions. A package cannot offer `Size` for both `String` and
`[]T`; the names have to differ, as in Go.
