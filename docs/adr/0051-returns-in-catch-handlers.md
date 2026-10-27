# ADR 0051: A bare `return` in a `catch` of a fallible lambda warns

**Status:** Accepted (2026-10-27). Adds warning W0013. Not breaking.

## Context

In a lambda that can fail (a `throws` function type, or a Go callback
returning `error`), reaching the end or a bare `return` means success. Inside
a `catch` handler that reads as giving up, and drops the error:

```kotlin
pgx.BeginFunc(ctx, pool) { tx ->
    debit(tx, from, amount)?
    credit(tx, to, amount) catch (err) {
        log.Warn("credit failed", "err", err)
        return              // success: BeginFunc commits the debit alone
    }
}?
```

Go does not compile the equivalent: a `func() error` makes the author write
`return nil` or `return err`. In Kotlin, a bare `return` in a lambda leaves
the enclosing function, or does not compile. The pre-announcement review
found it (S11) and asked for a warning.

## Decision

- **Warning W0013** on a bare `return` inside a `catch` handler of a fallible
  lambda without results: "return in this catch handler ends the lambda
  successfully, and the error is dropped". Hints: `throw err` to fail, and
  `catch (_)` to succeed on purpose.
- **`catch (_)` silences it.** Naming the error `_` says that it is dropped
  on purpose. A handler without a binder warns, as one that names the error
  does.
- **Scope.** Lambdas only, where the error goes to a helper the author does
  not control. A `throws` function's own `catch { return }` reads as what it
  is. Nested lambdas and `go` and `defer` blocks have returns of their own.
  A lambda with results cannot have a bare `return`.

## Alternatives

- **An error.** A unit lambda has no other way to say "succeed", so intended
  returns would need restructuring, and code that compiles would break.
- **`return` in a handler re-throws the error.** A hidden meaning for
  `return` that holds nowhere else.

## Consequences

The transaction bug becomes a warning at the line, with both ways out, and
nothing changes meaning. New checks start as warnings, as the stability
review asks.

### Implementation
- **Checker.** `bareCatchReturns` in `internal/check/lambda.go`, after a
  fallible lambda without results is checked.
- **Tests.** `check/catchreturn_warn` (warned and quiet cases),
  `run/optionalcopy` (`catch (_)` returning success).
