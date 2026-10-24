# ADR 0004: Lyth checker first, `go/types` validation second

**Status:** Accepted

## Context
Diagnostics must be in Lyth terms at Lyth spans, but a young checker will miss
cases, and silently passing invalid Go to `go build` would produce confusing
errors.

## Decision
1. The Lyth checker is the primary source of diagnostics.
2. After emission, each generated package is type-checked in-process with
   `go/types`, reusing the loaded dependency types. Errors are mapped through the
   exact source map and reported as E0902, with the original Go message kept.
3. Only then is `go build` invoked. Its errors (rare by then) are mapped
   through `//line` (E0903/E0904).

## Alternatives
- **Rely on `go build` only**: text parsing, line-level positions, slow feedback.
- **Trust the Lyth checker fully**: unsafe while it matures.

## Consequences
A safety net that turns checker gaps into located diagnostics. It costs a
second, fast type check per package and keeps the checker honest in tests,
because every golden program must pass validation.
