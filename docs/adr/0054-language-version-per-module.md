# ADR 0054: A module declares the Lyth version it is written for

**Status:** Proposed (2026-10-28). Needed before 1.0 so that changes after
it can be opt-in; not implemented.

## Context

After 1.0, a change that would stop working code from building, or change
what it means, cannot be made for everyone. Go gates such changes with the
`go` line of `go.mod`: loop variables became per-iteration only for modules
that declare `go 1.22`. Lyth has no equivalent, and needs one more than Go
does: since [ADR 0053](0053-lyth-libraries-across-modules.md), the installed
`lyth` checks the Lyth sources of every dependency, including libraries
written for an older Lyth.

The version must travel with the module (in the module zip), survive the
`go` command editing `go.mod`, and be readable without running anything.

## Decision (proposed)

- **Where.** A comment directive in `go.mod`, after the `go` line:

  ```text
  module example.com/app

  go 1.26

  //lyth:version 1.0
  ```

  The `go` command rejects unknown directives, so the version cannot be a
  directive of its own, but it keeps comments: `go mod tidy`, `go mod edit`,
  and `go get` preserve this line (checked with Go 1.27). `go.mod` is in every
  module zip, so dependencies carry it.
- **Meaning.** The module's Lyth code is checked with the rules of that
  version. A change after 1.0 that could break code applies only to modules
  that declare the version that introduced it or a newer one; a dependency is
  checked with its own declaration.
- **Missing.** A module without the line is written for 1.0, the first
  version with a promise. Before 1.0, nothing is gated and the line is only
  recorded.
- **Newer than the compiler.** A module that declares a version newer than
  the installed `lyth` is an error at the `go.mod` line (E0602), naming both
  versions and the fix, upgrading `lyth`. For a dependency, it is W0014: the
  program uses the dependency's published Go.
- **Writing it.** `lyth init` writes the installed version. Nothing raises it
  implicitly: `lyth fix -lang=1.N` raises it and applies the fixes for the
  changes up to 1.N, as `go fix` does for Go's.
- **New checks.** New compile-time checks still start as warnings
  ([compatibility](../guide/compatibility.md)); a check that becomes an error
  does so only from the version that makes it one.

## Alternatives

- **A file of its own** (`lyth.mod`). It also travels with the module, but
  is one more file in every project for one line, and editors and tools
  would have to find it. A `go.mod` comment sits beside the `go` line, which
  has the same role.
- **A directive per file** (`//lyth:version 1.2` at the top of a file). Go
  allows per-file versions with `//go:build go1.22` only to lower the
  version; a per-module version is what changes are reasoned about.
- **No version, only `lyth fix`.** Works for respellings, not for changes of
  meaning, and not for dependencies, which the program cannot rewrite.

## Open points

- Whether `lyth fix -lang` is a flag of `lyth fix` or `lyth mod edit -lyth`.
- Whether W0014 for a dependency newer than the compiler should be an error
  when the program needs the dependency's Lyth view to compile.
