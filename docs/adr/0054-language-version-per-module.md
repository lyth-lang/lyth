# ADR 0054: A module declares the Lyth version it is written for

**Status:** Accepted (2026-10-28). Needed before 1.0 so that changes after
it can be opt-in.

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

## Decision

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
  version with a promise, and so is a module that declares a version before
  1.0. Before 1.0, nothing is gated and the line is only recorded.
- **Newer than the compiler.** A module compiled with the program (the main
  module, a workspace module, a module replaced by a directory) that declares
  a version newer than the installed `lyth` is an error at the `go.mod` line
  (E0602), naming both versions and the fix, upgrading `lyth`; so are a
  malformed line and a second one. For a dependency from the module cache, it
  is W0014: the program uses the dependency's published Go, and code that
  needs its Lyth declarations fails with the errors W0014 explains.
- **Writing it.** `lyth init` writes the installed version (`LangVersion`,
  the major and minor of the release). Nothing raises it implicitly:
  `lyth fix -lang=1.N` raises it, before compiling, so the fixes for the
  changes up to 1.N apply, as `go fix` does for Go's. It refuses to lower
  the version or to raise it past the installed `lyth`.
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

Two questions were settled with the decision:

- **A flag of `lyth fix`, not a `lyth mod edit -lyth`.** Raising the version
  is worth doing only with the fixes it brings, and `lyth fix` is the command
  that rewrites code after an upgrade.
- **W0014, not an error, for a dependency newer than the compiler.** Its
  published Go builds, so a program that uses only what Go sees still builds;
  one that needs the Lyth declarations fails anyway, and W0014 at the import
  says why. An error would let one library release stop every program that
  depends on it until each upgrades `lyth`.

## Consequences

- The line is in `go.mod` of every module `lyth init` creates; existing
  modules gain it with `lyth init` or `lyth fix -lang`, and need nothing
  before 1.0.
- The first change after 1.0 that is gated passes the version of the
  package's module to the checker; until then nothing reads it but the
  checks above.
- `LangVersion` is bumped with `devVersion` after each release
  (`TestLangVersionFollowsDevVersion`).
- Tested by `TestLythVersionDirective` (newer, malformed, and duplicate
  lines; a replaced module; `go mod edit` keeps the line),
  `TestLythLibraryFromOtherModule` (a dependency written for a newer Lyth),
  `TestInit`, and `TestFixLang`.
