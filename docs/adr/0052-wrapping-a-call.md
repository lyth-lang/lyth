# ADR 0052: `f().wrap("context")` wraps a call's error when it fails

**Status:** Accepted (2026-10-27). Extends the prelude's `err.wrap` (errors.md
§4.3). Breaking only for `.wrap` on a call that returns nothing but an error,
used as a value.

## Context

Adding context to an error took three lines:

```kotlin
data := os.ReadFile(path) catch (err) {
    throw err.wrap("reading config ${path}")
}
```

A bare `?` takes one, so it wins: the repository's examples propagate with a
bare `?` 48 times, while the examples and benchmarks together wrap 3 times,
and production errors end up without context. Rust settled the same tension with `.context("…")?`.

Writing `.wrap` on the call was already possible in one case, with the wrong
meaning: on a call that returns only an error, `validate().wrap("ctx")` was
the error value wrapped as is, so a nil error (success) became a non-nil
`"ctx: %!w(<nil>)"`.

## Decision

- **`f().wrap(msg)` on a call that can fail** is that call with its error
  wrapped when it fails, and is still a call that can fail:

  ```kotlin
  data := os.ReadFile(path).wrap("reading config ${path}")?
  ```

  lowers to the Go of the `catch` form above:

  ```go
  data, err := os.ReadFile(path)
  if err != nil {
  	return "", fmt.Errorf("reading config %s: %w", path, err)
  }
  ```

  The message is evaluated only on failure. A call that returns only an
  error is the same: when it returns nil, nothing is wrapped.
- **Handled where it is written.** The error is wrapped in the branch that
  handles it, so the call is the operand of `?` or of an untyped `catch`,
  whose binder is then the wrapped error. Elsewhere (a typed `catch`, a
  pass-through `return`, destructuring, a value) it is E0216, with hints for
  `?`, `catch`, and for wrapping an error value (`e := f()`, then
  `e.wrap(...)`).
- **`err.wrap(msg)` on an error value is unchanged.**

## Alternatives

- **A new operator or keyword** (`f() ? "context"`, `f() catch wrap "…"`):
  more syntax for what a method reads as plainly.
- **Allow it everywhere** (destructuring, typed `catch`, pass-through): each
  form would need its own wrapping branch for little use; the handler of a
  typed `catch` can call `e.wrap(...)`.
- **Status quo**: errors keep arriving without context.

## Consequences

- Context costs a few characters per call, in the Go a careful programmer
  writes.
- `f().wrap(msg)` used as a value on a call that returns only an error is now
  E0216; the guide's version table has a row. Nothing in the repository used
  it.

### Implementation
- **Checker.** `selector` in `internal/check/member.go` types `.wrap` on a
  call that can fail as that call (`PreludeWrapFailure`); `call` reports it
  outside `?` and an untyped `catch` (`wrapHandled`).
- **Lowering.** The call lowers to itself; `tryInto` wraps the error in the
  failure branch and `catchHandler` assigns the wrapped error to the binder
  (`wrapOnFailure`, `wrapErr` in `internal/lower`).
- **Tests.** `run/wrapcall` (propagation, catch, a message evaluated only on
  failure, an error-only call that succeeds, `errors.Is` through the wrap),
  `gen/wrapcall` (the generated Go), `check/wrapcall_neg` (the places it is
  rejected).
