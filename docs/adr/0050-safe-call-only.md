# ADR 0050: `?.` is only the safe call

**Status:** Accepted (2026-10-27). Amends errors.md §3.3 and language.md §5.4.
Breaking, with a fix: `f()?.name` on a call that can fail is E0008, and
`lyth fix` rewrites it to `(f()?).name`.

## Context

`?.` had two meanings. On a nullable value, `x?.name` is the safe call: null
when `x` is null. On a call that can fail, `f()?.name` meant `(f()?).name`:
return the error from the enclosing function, then select. The checker could
always tell them apart, and rejected the case where both applied (a `T?
throws` result needs `??.`). A reader could not: whether `load(path)?.Name`
may return from the function depends on the signature of `load`, and Kotlin,
TypeScript, Swift, and C# readers know only the first meaning. Lyth's pitch
is code that is easy to review, and a hidden return point is the opposite.
The roadmap records a compiler bug that came from the same overlap
(`find()?.Name` on a nullable success value skipped the null check).

The propagating form is rare: once in the repository's examples and
benchmarks.

## Decision

- **`?.` is always the safe call.** To propagate and then select, write
  `(f()?).name`, or bind the value first (`v := f()?`). `f()??.name` stays:
  `?` propagates, then `?.` calls safely.
- **Migration.** `f()?.name` on a call that can fail is E0008, "?. is a safe
  call, and f(...) can fail rather than be null", whose edit `(f()?).name`
  is a fix: the respelling keeps the meaning, and after this change the old
  form can mean nothing else. `lyth fix` applies it; a second run changes
  nothing. The guide's version table has a row.

## Alternatives

- **Keep both meanings.** No churn, but every `?.` after a call is a
  possible return point that only the callee's signature reveals.
- **Swift's `try f().name`.** Unambiguous, but it respells every `?` in every
  program for a rare case.

## Consequences

- Every `?` that stands alone is a point where the function may return; `?.`
  never is.
- `(f()?).name` costs two parentheses in a rare case.

### Implementation
- **Checker.** `selector` in `internal/check/member.go` reports the old form
  with its fix and goes on checking it as `(f()?).name`, so one diagnostic
  reports it.
- **Tests.** `fix/safecall` (the rewrite), `check/optionalcopy_neg`
  (the diagnostic), `run/optionalcopy` (`(f()?).Name` and `f()??.Name`).
