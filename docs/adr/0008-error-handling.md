# ADR 0008: Error handling with `throws`, `?`, `catch`, `throw`

**Status:** Accepted

## Context
Error plumbing is Go's largest source of boilerplate. Lyth must keep Go's
error-as-value model, Go-compatible signatures, explicit control flow, and the
distinction between failure and absence.

## Decision
`throws` marks fallible functions (trailing Go `error` result). Postfix `?`
propagates unchanged errors from any call whose last result is `error`, Lyth or
Go. `catch (err) { ... }` handles locally with a value-or-diverge block.
`throw e` returns `zero, e`. `err.wrap(msg)` lowers to `fmt.Errorf("%s: %w")`.
Fallible lambdas are inferred from expected function types. Discarding an error
requires explicit `_ =`. Full semantics: spec/errors.md.

## Alternatives
- **Infer fallibility from `?` usage** (as in the original sketch): signatures
  would change with implementation details. Rejected.
- **`Result<T>` type**: needs a runtime representation or awkward erasure.
  Rejected.
- **Raw `(T, Error)` tuples only**: transparent but verbose (`return v, null`).
  Kept only for interface contracts like `io.Reader`.
- **Exceptions / try-catch blocks**: hidden control flow. Rejected.
- **`check`/`handle` function-level handlers** (Go 2 draft): action at a
  distance. Rejected.
- **Wrap-message suffix on `?`**: no clean syntax found. `catch` + `wrap` covers it.

## Consequences
Generated code equals hand-written Go, so there is no overhead. `errors.New`
and `ctx.Err()` stay ordinary value-returning functions, because `?` is an
operator on results rather than a function property. `x?.y` on a fallible `x`
means propagate-then-access (spec/errors.md §3.3).
