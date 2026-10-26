# ADR 0045: Go's evaluation order, with and without hoisting

**Status:** Accepted (2026-10-26). Replaces the evaluation-order rule of
language.md §6. Breaking only for code that reads a variable which a call in
the same expression changes, and only where that call was hoisted (a `?`,
`catch`, or `if`/`when` value in a later operand).

## Context

language.md §6 promised that Lyth evaluates operands "left to right,
completely", stricter than Go. The compiler kept that promise only when it
hoisted part of an expression into statements: there, it copied the operands
before the hoisted call into temporaries. Expressions without hoisting were
emitted unchanged, and the go compiler evaluates them its own way: it runs
the calls of an expression in lexical order and reads plain operands
(variables, fields, elements) after them. So the same expression gave two
answers:

```kotlin
var total = 1
func bump(): int {
    total = 100
    return 1
}
// total + bump()   was 101: Go reads total after the call
// total + bumpF()? was 2:   Lyth copied total before the hoisted call
```

The copies also broke a common pattern. A pointer method called on a field,
`self.stats.record(parse(s)?)`, copied the field into a temporary before the
argument, called the method on the copy, and lost the update; with a
`strings.Builder` field the program panicked.

Two ways out:

- **Strict left-to-right everywhere.** Every read of state a later call could
  change (a package variable, a field through a pointer, a captured local)
  would be copied first, also without hoisting. `self.items = append(self.items,
  newItem())` would become `v := self.items; self.items = append(v, newItem())`.
  The generated code would stop reading like Go, and behave unlike the Go a
  reader would write.
- **Go's order everywhere.** Hoisting keeps the order of calls, and leaves
  plain reads where Go puts them.

## Decision

Lyth evaluates expressions in Go's order:

- Calls, method calls, and channel receives run in lexical left-to-right
  order. The call of a `?` or `catch` is one of them, and so are the calls in
  an `if` or `when` used as a value.
- Plain operands are read as the go compiler reads them, after the calls of
  their expression.
- When lowering hoists a call into statements, the operands before it that
  call something are evaluated into temporaries first, so the calls keep their
  order. An operand that calls nothing is not copied: `total + bumpF()?` reads
  `total` after `bumpF`, as `total + bump()` does, and
  `self.stats.record(parse(s)?)` calls `record` on the field itself.
- A receiver that does call something and whose method takes its address is
  saved as that address (`v := &get().c`), never as a copy.

Code should not depend on a variable that a call in the same expression
changes. Go leaves that order unspecified; Lyth follows the go compiler,
which is what the generated code runs on.

## Consequences

- The result of an expression no longer depends on whether part of it is
  hoisted: adding a `?` never changes what the rest reads.
- The generated Go has fewer temporaries, and reads like hand-written Go
  (`out = append(out, v)` after the check, not `v2 := out`).
- Short-circuit evaluation is unchanged: the right side of `&&`, `||`, and
  `?:` still runs only when needed.
- `run/order` (call order and short-circuiting) and `run/spills` (plain
  reads, receivers, catch handlers) pin the behavior.
