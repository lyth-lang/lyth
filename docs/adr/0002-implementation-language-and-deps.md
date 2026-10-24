# ADR 0002: Compiler written in Go; minimal dependencies

**Status:** Accepted

## Context
The compiler needs Go's type checker, AST, printer, and constant arithmetic, and
must ship as an easy-to-install binary for Go developers.

## Decision
Write the compiler in Go. Allowed external dependency: `golang.org/x/tools`
(for `go/packages`), which transitively adds `x/mod` and `x/sync`. Lexer,
parser, checker, and diagnostics are hand-written.

## Alternatives
- **Kotlin/JVM**: familiar syntax tooling, but no access to `go/types`, and a
  JVM dependency for Go developers. Rejected.
- **Rust**: excellent compiler ergonomics, but would need to reimplement or
  shell out for all Go type information. Rejected.
- **Parser generators (ANTLR, goyacc)**: weaker error recovery and diagnostics,
  and newline-sensitive grammar handling is awkward. Rejected.

## Consequences
Direct use of `go/types`, `go/constant`, `go/printer`, `go/build`. One static
binary. The x/tools version must track supported Go releases (ADR 0003).
