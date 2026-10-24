# ADR 0027: Only `throws` and Go callbacks make a lambda fallible

**Status:** Accepted (refines 0008)

## Context
ADR 0008 infers fallible lambdas from their expected function type, so that
`g.Go { fetch(ctx, url)? }` works for Go's `func() error`. The rule was "the
type's last result is `Error`", which also matched function types written in
Lyth. A lambda passed as a `(Int) -> Error?` was lowered like a
`(Int) -> Unit throws`: its last expression was evaluated as a statement and
the lambda returned nil. `val lookup: (String) -> Error? = { missing(it) }`
compiled and always reported success, and `{ validate(it) }` was rejected as
discarding an error that the programmer meant to return. A `fun` declared
with the same type returns its value, so lambdas and named functions
disagreed.

## Decision
- A lambda is fallible when its expected type is declared `throws`, or is a
  Go function type whose last result is `error`. Go has no `throws`; a
  trailing `error` is how a Go callback says it can fail (`func() error`,
  `fs.WalkDirFunc`), so those keep the ADR 0008 behavior: `?` and `throw`
  work, and the success values precede the error.
- A function type written in Lyth follows the rules of a `fun` with that
  signature. An `Error` or `Error?` result is a value the lambda returns. A
  tuple ending in `Error?` is a raw tuple (errors.md §2.3): `return n, null`
  returns exactly that, and `?` returns zero values with the error.
- `?` or `throw` in a lambda whose Lyth type returns an error is E0301 or
  E0304, with a note that the error is a value and a hint to return it or to
  declare the type `throws`.
- Calls are unaffected: calling a function value whose results end in `Error`
  still gives a fallible result that must be handled (errors.md §2.1).

## Alternatives
- **Keep the rule and warn when the value is discarded**: removes the silent
  failure, but keeps a meaning of `(Int) -> Error?` that differs between
  lambdas and named functions.
- **Treat every `Error` result as fallible, Lyth and Go alike**: consistent
  with Go's convention, but contradicts Lyth's rule that fallibility is
  declared with `throws` (ADR 0012), and leaves no way to write a lambda that
  returns an error value.

## Consequences
A Lyth `(T) -> Error?` and a `(T) -> Unit throws` lower to the same Go type,
`func(T) error`, and are called the same way; they differ only in how the
lambda body is written: the first returns an error value, the second fails
with `throw` or `?`. Go callback types are unchanged, so `errgroup`, `fs`, and
handler types keep their concise lambdas.
