# ADR 0020: Declared conformance: `struct T : I`

**Status:** Accepted

## Context
Interfaces in Go, and so in Lyth, are structural. When a type stops implementing
an interface (a method renamed, a signature changed, a receiver made a
pointer), the error appears at some distant assignment, or nowhere if the only
use is in another package. Go programmers guard against this with
`var _ Shape = Circle{}` or `var _ io.Writer = (*counter)(nil)`, an idiom that
works but reads as a trick, and whose error is a type mismatch rather than a
missing method.

## Decision
- A struct may list the interfaces it implements: `struct Circle : Shape,
  fmt.Stringer { ... }`. The list is optional and changes nothing about method
  sets, dispatch, or which types implement which interfaces.
- The checker verifies each entry once all methods of the package are known:
  `T` or `*T` must implement the interface. If neither does, E0212 at the entry
  names the method, the signature the interface needs, and the reason (missing,
  a different signature, not `pub`).
- Each entry lowers to Go's assertion: `var _ I = T{}`, or `var _ I = (*T)(nil)`
  when only the pointer type implements `I`.
- Only interfaces may be listed. Anything else is E0201, with a note that Lyth
  has no inheritance.

## Alternatives
- **Nominal interfaces** (declaration required): would break interoperability,
  where Go types implement Lyth interfaces and Lyth types implement Go interfaces
  without knowing about each other.
- **An annotation** (`@implements(Shape)`): Lyth has no annotations, and the
  header form is familiar from Kotlin, Swift, and Rust's `impl` blocks.
- **Writing Go's idiom in Lyth** (`val _: Shape = Circle()`): it works, but the
  intent is implicit and the diagnostic is generic.
- **Stating the receiver** (`struct T : *I`): unnecessary, since the checker
  knows which of `T` and `*T` implements `I`, and the generated assertion shows
  it.

## Consequences
Conformance becomes documentation that the compiler keeps true, at no cost
beyond Go's own idiom. Code that omits the list behaves exactly as before.
Struct embedding and generic structs, when they arrive, use the same header
without conflict, because only interfaces may follow the colon.
