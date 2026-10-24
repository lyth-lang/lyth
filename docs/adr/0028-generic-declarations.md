# ADR 0028: Generic declarations are Go generics

**Status:** Accepted; type parameters and arguments respelled `[T]` by [0031](0031-go-spelling.md)

## Context
Lyth could call generic Go functions and use instantiated Go types, but not
declare its own. Without that, helpers such as a typed `Page<T>` response or a
`maxOf` had to be written per type, and collection operations (ADR 0029, ADR
0030) could not be written in Lyth at all. The syntax (`fun <T: C>`,
`struct Page<T>`) was specified early so that editor support could rely on it.

## Decision
- Functions, structs, and interfaces declare type parameters:
  `fun <T: cmp.Ordered> maxOf(a: T, b: T): T`, `struct Page<T>`,
  `interface Container<T>`. They lower to Go type parameters one to one, and
  each Lyth type parameter is a `go/types` type parameter, so instantiation and
  constraint satisfaction are exactly Go's.
- Constraints are interfaces: `Any` by default, a predeclared `Comparable`, Go
  constraints such as `cmp.Ordered`, or method interfaces. Interfaces that Go
  allows only as constraints are rejected elsewhere.
- Type arguments are inferred from the arguments, the expected result type,
  and lambda results, or given in full (`zero<String>()`). Construction
  infers a struct's type arguments from its fields. The generated Go names
  type arguments only where Go cannot infer them (a type parameter that
  occurs only in the result, a composite literal).
- A generic struct's methods name its type parameters in the receiver
  (`fun Page<T>.First()`); methods cannot declare their own, as in Go.
- The zero value of a type parameter is Go's, even for type arguments that
  have no zero value in Lyth.

## Alternatives
- **Monomorphize in the Lyth compiler**: would allow generic methods and `T?`,
  but produces Go that no Go programmer would write, bloats binaries, and
  diverges from Go's semantics and tooling.
- **Kotlin-style variance (`in`/`out`) and reified type parameters**: neither
  exists in Go, and both need a runtime representation Lyth does not have.
- **Reject `var z: T` without an initializer**: sound, but `(T, Bool)` and
  `(T, error)` results, Go's idiom for "not found", would need contortions.

## Consequences
Generic Lyth code is generic Go code, callable from Go with Go's syntax. Lyth
inherits Go's limits: no generic methods (extension functions fill the gap),
no specialization. Nullable type parameters (`T?`) need a representation that
works for every type argument and remain open. Named arguments and default
values on generic functions are not supported yet.
