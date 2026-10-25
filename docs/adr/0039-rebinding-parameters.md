# ADR 0039: `:=` may replace a parameter with a value derived from it

**Status:** Accepted (2026-10-25). Amends [0007](0007-val-var-and-immutability.md)
and [0031](0031-go-spelling.md) (read-only parameters, `:=` lists).
Not breaking: it only accepts code that is an error today.

## Context

Parameters are read-only (ADR 0007, kept by ADR 0031). A `:=` list may reuse
only locals of the same scope, so the most common lines of Go service code are
errors:

```kotlin
func *Store.Load(ctx: context.Context, id: int64): Order throws {
    ctx, cancel := context.WithTimeout(ctx, 2 * time.Second)   // E0501
    defer cancel()
    ...
}
```

`error[E0501]: ctx cannot be assigned: := may reuse only local variables of
this scope`, with no hint. The same holds for `g, ctx :=
errgroup.WithContext(ctx)`, `ctx = metadata.AppendToOutgoingContext(ctx, …)`,
`r = r.WithContext(…)` in middleware, and `name = strings.TrimSpace(name)`.

All three probes of the review hit it.

- **The workaround brings back the bug.** The workaround is a second name
  (`tctx`), which leaves the parent `ctx` in scope. Passing the wrong one
  compiles and silently drops the timeout or cancellation, which is the bug
  Go's rebinding idiom exists to prevent.
- **Read-only parameters target something else.** They exist to catch a
  mistaken assignment (`discount = 20`) and the illusion of changing the
  caller's variable. Replacing a value with one derived from it is neither.

Swift (`let x = x`) and Rust (`let x = …x…`) allow exactly this: a new binding
that shadows the parameter.

## Decision

- **The rule.** In a `:=` declaration (single or list), a name that is a
  parameter of the enclosing function or lambda may appear on the left when
  the right side reads that parameter. The statement declares a new local with
  the parameter's name and type, which hides the parameter for the rest of the
  block. The parameter itself is never assigned, so parameters stay read-only.

  ```kotlin
  ctx, cancel := context.WithTimeout(ctx, 2 * time.Second)
  g, ctx := errgroup.WithContext(ctx)
  name := strings.TrimSpace(name)
  r := r.WithContext(context.WithValue(r.Context(), userKey, user))
  ```
- **The new local has the parameter's type.** A different type is an error
  ("id would change type from string to int; choose a new name"). A same-typed
  rebinding is what the idiom is for, and it keeps the generated line
  identical to Go.
- **Without a read, it stays an error.** If the right side does not read the
  parameter, the rule does not apply. `limit := 50` with a parameter `limit`
  stays an error (E0005), now with a hint ("this would ignore the argument
  limit; choose another name"), because ignoring an argument is a likely
  mistake.
- **No W0006.** The rebinding draws no shadowing warning; it is the intended
  idiom.
- **Plain assignment stays an error.** `ctx = f(ctx)` on a parameter stays
  E0501, now with the hint "write ctx := f(ctx) to replace the parameter for
  the rest of this block". It is not a fix: inside a nested block, Go's
  assignment would change the outer variable, and the hinted form does not.
- **Lowering.** The line lowers to Go's own line,
  `ctx, cancel := context.WithTimeout(ctx, …)`. In the function's outermost
  block Go assigns the parameter, which is unobservable unless a closure
  created earlier in the function captured the parameter. In that case, and
  only then, the lowerer renames the new local (`ctx2`), as it renames other
  temporaries.

## Alternatives

- **Allow assigning parameters (Go's rule).** Simplest, but it drops the check
  ADR 0007 kept on purpose (`discount = 20`), and the value-receiver copy rule
  rests on the same idea.
- **Only `:=`, without the "reads the parameter" condition.** It is more
  permissive, but `limit := 50` silently ignoring the argument is exactly the
  mistake read-only parameters are meant to catch.
- **Allow type changes (Rust's shadowing).** `id := strconv.Atoi(id)?` reads
  well, but the generated Go needs a renamed variable at the top level, the
  idiom is foreign to Go readers, and a new name is clearer when the meaning
  changes.
- **Keep the error, add a hint for a new name.** That documents the workaround
  that leaves the parent context in scope.

## Consequences

- **Go code compiles unchanged.** The context, errgroup, and request-rebinding
  idioms compile as written in Go, and the generated Go is the same line.
- **Parameters stay read-only.** Only their names can be replaced, by a
  derived value of the same type. Smart casts of the new local follow the
  usual rules for locals.
- **What the guide says.** The "Coming from Go" table gains this row, and the
  E0501 and E0005 messages gain hints.

### Migration
None: code that compiles today keeps its meaning.

### Implementation
- **Checker.** `destructure` and the single-declaration path in
  `internal/check/stmt.go`:
  - a parameter in the same scope becomes a new local when the right side
    reads it;
  - add the type check and the two hints.
- **Lowering.** Rename the new local only when a closure earlier in the
  function captures the parameter.
- **Tests.**
  - `run/rebind`: context, errgroup, request, nested blocks, earlier capture.
  - `check/rebind_neg`: type change, no read, plain assignment.
  - `gen/rebind`: the generated line matches Go.
