# ADR 0018: Typed `catch` clauses match with `errors.AsType`

**Status:** Accepted (extends 0008)

## Context
Backend code handles specific errors all the time: a missing row becomes a
404, a unique-constraint violation a 409, a timeout a retry. Go code writes
`var nf *NotFound; if errors.As(err, &nf) { ... }`, or since Go 1.26
`if nf, ok := errors.AsType[*NotFound](err); ok { ... }`, and must remember to
return every other error. With only `catch (err)`, Lyth code wrote the same
dispatch by hand inside the handler, including the easy-to-forget
`else { throw err }`.

## Decision
- `e catch (x: T) { ... }` handles errors of type `T`, a non-null type that
  implements `Error`, and binds `x` as a `T`. Several clauses may follow one
  another and are tried in order.
- Matching uses `errors.AsType[T]`, so it sees through wrapping. That differs
  from `is` (ADR 0016) on purpose: `is` asks about a value's dynamic type, while
  `catch` asks about an error, and Go inspects errors through their chain.
- Errors that no typed clause handles are still errors. The expression stays
  fallible and must end with `?` (propagate the rest) or with an untyped
  `catch` (handle the rest). Otherwise it is E0303.
- The chain lowers to one `if err != nil` holding an `if`/`else if` dispatch on
  `errors.AsType`, ending in the final handler or in `return ..., err`.

## Alternatives
- **Unmatched errors propagate implicitly**, as in Java or Kotlin: shorter, but
  it hides an exit point. In Lyth every propagation is visible as `?` (ADR 0008).
- **`when` over errors** with `is`/`errors.Is` conditions: more general, but it
  moves the handling away from the call and needs the error bound first. It
  remains possible later for sentinel values.
- **Type assertions (`err.(T)`)**: miss wrapped errors, which are the common
  case once code uses `wrap`.
- **Sentinel patterns (`catch (ErrNotFound)`)**: sentinels compare with
  `errors.Is`, and a value in the binder position would be ambiguous. An
  untyped `catch` with `errors.Is` covers them.

## Consequences
The generated code is the dispatch a Go programmer writes, with no helper and
no allocation beyond `errors.AsType`'s own. It needs Go 1.26, which Lyth already
requires. A typed clause whose binder is unused names it `_`
(`catch (_: *NotFound) { fallback }?`), following the rule for every unused
name.
