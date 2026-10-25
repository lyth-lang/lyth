# ADR 0025: Enums and sealed interfaces

**Status:** Accepted; enums on the wire and string-backed enums added by [0035](0035-enums-on-the-wire.md)

## Context
Closed sets of values and closed sets of types are the feature most often
missed by Go programmers, and the editor support (completion, exhaustiveness)
depends on their grammar. `enum` and `sealed` are already reserved words. The
spelling is fixed now so that the LSP and editor grammars are written once.

## Decision
- **Enums.** `enum Color { Red, Green, Blue }` declares a named integer type
  and one constant per entry, lowered to Go's `iota` idiom with a `String()`
  method:

  ```go
  type Color int

  const (
      ColorRed Color = iota
      ColorGreen
      ColorBlue
  )
  ```

  Lyth code names entries through their type, `Color.Red`; the Go constant is
  the type name followed by the entry name, Go's usual convention, which keeps
  entries of different enums from colliding in the package scope. Inside
  `when` on an enum subject, entries may be written unqualified
  (`Red -> ...`). The zero value is the first entry. Visibility follows the
  type's name (ADR 0022).
- **Sealed interfaces.** `sealed interface Shape { ... }` is an interface that
  only types of the same package can implement, and only by declaring it
  (`struct Circle : Shape`, ADR 0020). It lowers to a Go interface with an
  unexported marker method (`isShape()`), which each declared member gets.
- **Exhaustiveness.** `when` over an enum or sealed interface needs no `else`
  when every entry or member type has a branch; adding one later is a compile
  error at each `when` that misses it. The lowered Go `switch` gets a
  `default` that panics with the Lyth position, since Go cannot see that the
  switch is exhaustive.
- **Settled by the implementation.** A `when` over a non-null enum or sealed
  subject must be exhaustive or have an `else`, as a statement too (Kotlin's
  rule): that is what makes a new entry or member a compile error everywhere
  it matters. Entries resolve unqualified in a `when` over a nullable enum as
  well. A member is `*T` when only the pointer has the methods. Enums get
  `==`, not ordering; `Int(c)` and `Color(n)` convert as in Go, and JSON
  encodes the number. In `fun Circle.Kind(): Kind = Kind.Round`, `Kind.Round`
  names the enum's entry although `Kind` alone would be the method: a method
  value has no members.

## Alternatives
- **Kotlin's `enum class` and `sealed class`**: implies classes and
  inheritance, which Lyth does not have.
- **Entries as unqualified package constants** (`Red`): what plain Go does,
  but entries of different enums collide, and Go code usually prefixes them
  anyway.
- **Enums with associated values** (Swift, Rust): covered by sealed
  interfaces whose members are structs, with no new runtime representation.

## Consequences
The Go name of an enum entry differs from its Lyth spelling (`Color.Red` versus
`ColorRed`), a deliberate, mechanical exception to ADR 0022 that diagnostics
and documentation spell out. Sealed interfaces cost one unexported method per
member type and nothing at run time. `enum` and `sealed` became keywords.
