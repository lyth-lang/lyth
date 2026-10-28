# ADR 0053: Lyth libraries from other modules keep their Lyth declarations

**Status:** Accepted (2026-10-28). Amends [ADR 0006](0006-overlay-builds.md)
for dependencies; the language version a dependency is checked with is
[ADR 0054](0054-language-version-per-module.md).

## Context

A Lyth package is a Go package, so a Lyth library is published as a Go
module: its `.lyth` sources and the Go that `lyth gen -in-tree` writes beside
them, which Go programs build. Lyth code saw such a library only through that
Go, unless it was a package of the same module or of a `go.work` workspace:

```kotlin
import "example.com/lib/users"

func describe(id: int): string {
    u := users.Find(id) ?: return "nobody"
    email := u.Email ?: "none"              // E0201: Email was a *string
    return users.Greet("${u.Name} ${email}") // E0203: the default argument was gone
}
```

`string?` arrived as `*string`, default and named arguments were gone, and a
`when` over an enum or sealed interface of the library needed an `else`.
Without the Lyth view there is no Lyth library API to keep stable (the
stability review's 2.1).

Two facts shape the design. The `go` command refuses an overlay for a file
under `GOMODCACHE` ("Files beneath GOMODCACHE must not be replaced"), so a
dependency from the module cache builds from the Go it published, never from
Go that Lyth generates during the build. And `go.mod` lists, since Go 1.17,
every module that provides a package the module's build imports, so it names
the module and directory of every import.

## Decision

- **A module replaced by a local directory** (`replace example.com/lib =>
  ../lib`) is compiled with the program, as a workspace module already is:
  its Lyth packages are checked, generated into the program's
  `.lyth/gen/_dep/<module>/`, and overlaid in their own directory. Nothing is
  written into the replaced module.
- **A module from the module cache** that has `.lyth` files in an imported
  package is checked from those sources, so the program sees its Lyth
  declarations, and is built from the Go published with them. Lyth finds the
  module through `go.mod` (`require`, and `replace` by another module) and
  the module cache, downloading it as the `go` command would. Its tests are
  not compiled, and its diagnostics are its own: a dependency's warnings do
  not reach the program.
- **The two must agree.** The exported API the sources lower to (types,
  their methods, functions, extension functions) is compared with the
  published Go's. If they differ, or the sources do not compile with the
  installed `lyth`, the program sees the published Go, as before this ADR,
  and each import of the package draws **W0014** naming the reason: the first
  error of the sources, or the first declaration that differs. A package with
  `.lyth` sources and no published Go is E0103 at the import, with a note
  saying that a Lyth library publishes the output of `lyth gen -in-tree`.

## Alternatives

- **A metadata file** (export data for Lyth types) published with the
  library. It would need a format, a version, and a way to generate and check
  it; the `.lyth` sources already are that information, and the compiler
  that reads them is the one that builds the program.
- **Generating the dependency's Go during the build.** Impossible for the
  module cache (no overlays there), and copying the module elsewhere would
  change the import paths Go code sees.
- **An error instead of W0014** for sources that no longer compile or do not
  match. The published Go builds, so the program can too; failing would let
  an old library stop every program that uses it after a `lyth` upgrade.

## Consequences

- A Lyth library's API is its Lyth declarations for Lyth callers and its Go
  for Go callers; [compatibility](../guide/compatibility.md) lists which
  changes break which.
- Checking a dependency's sources costs time on each build, as checking the
  program's own packages does; the dependency is not lowered or emitted.
- The installed `lyth` checks a dependency written for an older Lyth, so the
  language version the dependency declares decides which rules apply
  ([ADR 0054](0054-language-version-per-module.md)).
- Tested by `TestLythLibraryFromOtherModule`: a local replacement, the
  module cache, a package without published Go, published Go that does not
  match, and sources this `lyth` rejects.
