# ADR 0021: Inferred results for private expression bodies; pass-through `return`

**Status:** Accepted (refines 0008)

## Context
Two small frictions came up repeatedly in backend code.

1. Private helpers such as `fun isSuccess(code: Int): Bool = code in 200..299`
   repeat a type that the body already states. Kotlin infers the result of
   expression bodies, and the spec reserved this as a compatible extension.
2. In a `throws` function, returning another fallible call needed
   `return f()?`. Besides reading worse than Go's `return f()`, it returns zero
   values on failure, which breaks contracts such as `io.Writer`: a wrapper's
   `Write` must report the bytes written before an error.

## Decision
- A private function or method with an expression body may omit its result
  type. It is inferred from the body on demand, when a call or another body
  first needs it, so declaration order does not matter. A result that depends
  on itself must be declared (E0208).
- `pub` functions always declare their result type (E0208), because Go callers,
  documentation, and API review read it. A fallible body must declare
  `throws` and its result, since fallibility is never inferred (ADR 0012). A
  body producing several values declares them too.
- In a `throws` function, `return f()` where `f()` is fallible and its success
  values have the function's result types, or are interface values, lowers to
  Go's `return f()`: every result, the error included, passes through
  unchanged. A concrete value that would become an interface does not pass
  through, because a failing call's nil pointer would become a non-nil
  interface (the typed-nil trap); such code writes `return f()?`.

## Alternatives
- **Inference for every function**: changes a package's API whenever a body
  changes, invisibly to Go callers. Kotlin recommends explicit types on public
  API for the same reason.
- **Inference for block bodies**: several `return` statements must agree,
  and the type is harder to see when reading the function.
- **Always requiring `?` on returned calls**: one rule fewer, but it loses
  partial results and is not how Go code reads.
- **Pass-through with any assignable type**: reintroduces the typed-nil trap
  that Lyth avoids elsewhere.

## Consequences
Inference needs on-demand checking of function bodies with cycle detection,
and a private helper's type can change with its body, which only affects its
own package. `return f()` and `return f()?` now differ on failure: the first
returns the callee's values with the error, the second zero values.
