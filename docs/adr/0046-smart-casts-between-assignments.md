# ADR 0046: Smart casts between assignments

**Status:** Accepted (2026-10-26). Amends the smart-cast rule of
[ADR 0031](0031-go-spelling.md) and types.md §5.3; makes the error of a
destructured call `error?`, as errors.md §5 always said.

## Context

ADR 0031 let a local smart-cast only when the function never reassigns it.
The rule is simple and sound, but it rejects Go's most common error idiom
once the error has its documented type `error?`:

```kotlin
x, err := strconv.Atoi(a)
if err != null {
    throw err
}
y, err := strconv.Atoi(b) // := assigns err again
if err != null {
    throw err.wrap("second") // err was not smart-cast: E0305
}
```

The compiler had kept the idiom working by typing the error as a platform
`error!`, which let `throw err` compile without a check and throw a nil
error. Walking a linked list (`node = node.Next` in `while node != null`)
and defaulting a value (`if u == null { u = guest }`) failed the same way.

## Decision

A check smart-casts a reassigned local until its next assignment:

- Every assignment (`=`, a `:=` list that reuses the name, `++`/`--`) ends
  the smart casts of the variable. An assignment of a value that is never
  null smart-casts it to non-null, and so does a declaration
  `x: T? = nonNull`.
- A loop ends, at its start, the smart casts of the variables its body
  assigns, since the next iteration runs after those assignments; the
  condition of `while node != null` smart-casts `node` again each time.
- After an `if`/`else` or a `when`, a variable stays smart-cast when every
  branch that continues leaves it non-null; an `if` without `else` and a
  `when` without one count the path that runs no branch.
- The rule applies only where the compiler sees every assignment in order.
  A local whose address is taken (`&x`), that a lambda, `go` block, or
  `defer` block assigns, or that an `if`, `when`, or `catch` used as a value
  assigns (such a block runs before the plain reads of the expression around
  it, [ADR 0045](0045-go-evaluation-order.md)) is never smart-cast between
  assignments. A smart cast made outside a lambda does not hold inside it.
- Locals the function never reassigns keep ADR 0031's rule, including
  inside lambdas.

`v, err := f()` binds `err` as `error?` for Go calls too, and a success
value that has no zero value as `T?`, null when the call fails (errors.md
§5). E0401 names the call in that case and suggests `v := f()?`.

## Consequences

- The Go idiom, list walks, and lazy defaults compile without copies, and
  `throw err` on an unchecked error is E0305.
- E0401 says why a check did not hold: an assignment after it, a loop, a
  closure, `&x`, or a lambda.
- Comparing a smart-cast variable with `null` is legal and warns (W0004); it
  compiles to Go's `x != nil` on the variable.
- `run/flowcasts` and `check/flowcasts_neg` pin the rule.
