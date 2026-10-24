# ADR 0001: Compile to Go source through `go/ast`

**Status:** Accepted

## Context
Lyth must reuse Go's runtime, toolchain, and ecosystem, produce readable output,
and keep exact source positions for diagnostics.

## Decision
Lower the typed Lyth AST directly to `go/ast`, print with `go/printer` (gofmt
configuration), and build with the official `go` command. `go/ast` is the
intermediate representation: there is no separate Lyth IR in v0.

## Alternatives
- **Emit Go text with a custom writer.** Exact position control, but the output
  is not gofmt-canonical without re-formatting (which loses positions), and
  injection and precedence bugs become string bugs.
- **Custom IR, then `go/ast`.** Useful for optimizations Lyth does not need yet,
  and would duplicate `go/ast`'s statement/expression forms. Can be inserted
  later between `check` and `lower`.
- **Compile to Go SSA or object code.** Loses readability, debuggability, and
  toolchain compatibility. Rejected.

## Consequences
Output is gofmt-canonical and syntactically valid by construction. Positions
come from a print → reparse → lockstep walk (ADR 0005), which also catches
lowering bugs. Verified: the printer inserts parentheses for precedence when a
`ParenExpr` is missing, so the walk skips `ParenExpr` nodes.
