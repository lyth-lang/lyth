# ADR 0041: Type tests on errors stay dynamic, and warn

**Status:** Accepted (2026-10-25). Confirms [0016](0016-type-tests.md)
and [0018](0018-typed-catch.md); adds a warning.
Not breaking.

## Context

The same question gets two answers, depending on how it is asked:

```kotlin
load(7) catch (err) {
    when err {
        is *NotFound -> "not found"       // false: err is a wrapped *NotFound
        else -> "other"
    }
}
load(7) catch (e: *NotFound) { … }      // matches: errors.AsType sees through wrapping
```

ADR 0016 made `is` a type assertion that sees the dynamic type only, and ADR
0018 made typed `catch` use `errors.AsType` on purpose. Each choice is right on
its own. But Lyth encourages wrapping (`err.wrap(…)`), so `err is T` and
`when err { is T -> }` on an error are almost always a bug in waiting: they
pass the tests that throw `T` directly and fail in production, where the
error arrives wrapped. The pre-announcement review listed two options: change
`is`, or warn. Working through the first showed it does not hold.

- **The smart cast would rebind the name.** After `if err is *NotFound`, `err`
  is smart-cast to `*NotFound`. With `errors.AsType`, the value found is the
  wrapped one, not `err`. So inside the branch `err` would silently mean a
  different value: `err.Error()` would print `not found 7` instead of
  `load 7: not found 7`, and logging `err` would lose the context.
- **One spelling, two meanings.** `x is T` would test the dynamic type for an
  `any` and the chain for an `error`. That is the hidden difference ADR 0016
  rejected.

## Decision

- **`is` stays a type assertion.** `is`, `!is`, `as`, `as?`, and `when … { is
  T -> }` keep ADR 0016's meaning on every operand.
- **New warning, W0007 (the next free warning code).** It fires when the operand's static
  type is `error` (or an interface that embeds it) and `T` is a type that
  implements `error`:

  > warning[W0007]: a type test sees only the dynamic type of err, so a
  > wrapped *NotFound does not match
  > hint: to match through wrapping, handle the call with
  > catch (e: *NotFound) { ... }, or bind it: e, ok := errors.AsType[*NotFound](err)
  > hint: to test the dynamic type on purpose, bind the error as any first:
  > v: any = err

  It is a hint, not a fix: the rewrite depends on the code around it.
- **Silencing it.** To test the dynamic type deliberately, for example in an
  `Unwrap` or `Is` method, bind the error as `any` first: `v: any = err`, then
  `v is *NotFound`. That silences the warning and says so in the code.
- **Later, `when` over errors.** A `when` form with chain semantics and its
  own binding stays a later, non-breaking feature. Its spelling needs to show
  that the bound value is the wrapped error, not the subject.

## Alternatives

- **`is` on errors uses `errors.AsType`.** It reads as intended in the common
  case, but it is unsound for the reason above: the smart-cast name would
  denote a different value than the one tested. The rewrite would also change
  the behaviour of code that compiles today.
- **Forbid `is` on errors.** This is too strong: `Unwrap`/`Is`
  implementations and errors from code that never wraps have legitimate uses.
- **Status quo.** The trap stays silent.

## Consequences

The trap becomes a warning with the exact alternative, and nothing changes
meaning. This ADR is not breaking, so it does not block the announcement. It is
listed with the decisions because the alternative (changing `is`) would have
been breaking, and the warning should ship before people learn the pattern.

### Implementation
- **Checker.** Add the warning in the type-test checking of `internal/check`.
- **Tests.**
  - `check/errortest_warn`: `is`, `as?`, and `when … is` on `error`, on `any`
    (no warning), and the silencing form.
  - The guide's error section and the tour gain a sentence.
