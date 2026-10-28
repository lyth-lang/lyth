# Compatibility

This page says what Lyth will keep stable across 1.x releases, what it leaves
free to change, and which Go releases a Lyth release supports. It is the
promise 1.0 will make; until then, the [section before 1.0](#before-10) says
what holds today.

## Before 1.0

The language is frozen on the way to 1.0: releases fix bugs and make only
compatible additions, with no new syntax. Where a fix has to reject code that
an earlier release accepted, the old form gets a diagnostic, with a fix that
`lyth fix` applies where the change only respells the code. Code written for
an older release is updated with `lyth fix`, and the
[changes between versions](writing-lyth.md#changes-between-versions) list every
change of syntax.

Pin the `lyth` version a project builds with, in CI and on every machine
(`go install github.com/lyth-lang/lyth/cmd/lyth@vX.Y.Z`), and upgrade on
purpose, as with any pre-1.0 tool.

## What 1.x keeps

A program that builds with a Lyth 1.x release builds with every later 1.x
release and means the same. In detail:

- **Syntax and semantics.** What the [specification](../spec/language.md)
  describes keeps its meaning. Forms that are replaced keep compiling for the
  rest of the major version, with a warning and a `lyth fix` fix.
- **New checks start as warnings.** A new compile-time check (an unhandled
  error case, a nullability rule, a smaller list of errors that may be
  ignored) could stop working code from building, so it arrives as a warning.
- **The Go API of exported declarations**, which Go code and other packages
  build against:
  - `func F(): T throws` is `func F() (T, error)`; a `T?` result or
    parameter is `*T`, `*T?` is `*T` too, and a nullable interface `Shape?`
    is `Shape`.
  - Enum entries are the constants `ColorRed`, ..., of `type Color int` with
    `String`, `MarshalText`, and `UnmarshalText`; a string enum is
    `type Plan string`.
  - An extension function `func string.Shout()` is the function `Shout(s)`.
  - Parameters with defaults are ordinary parameters: Go callers pass every
    argument, and Lyth callers get the defaults filled in at the call.
  - A sealed interface is a Go interface with an unexported marker method.
- **Wire formats.** Exported fields get lower-camel `json` keys, `T?` fields
  also `omitzero`, and enums travel as their names in text and JSON. Data
  written by one 1.x release is read by the next.
- **`lyth/coll`**: its functions, their results, and their documented
  behavior on empty input ([collections](../design/collections.md)).
- **The command line**: the commands and their flags, and `lyth guide` with
  its topic names, which every project's `AGENTS.md` relies on.
- **Diagnostic codes**: a code keeps its meaning and is never reused, and the
  `-diagnostics=json` objects only gain fields.
- **`lyth fmt`**: a file it has formatted stays formatted by later releases.

## What may change

- The wording of diagnostics, notes, and hints, and which edits they suggest.
- The generated Go of function bodies and of unexported declarations:
  temporary names, layout, and the exact statements, as long as the program
  means the same. Read it with `lyth gen`, but do not depend on its text.
- The Go packages under `internal/` in the Lyth repository, which have no
  API, the `.lyth/` build directory, and the type cache in it.
- Programs that depend on a compiler bug: code the specification rejects may
  stop compiling when the bug is fixed, with a diagnostic that says so.
- Performance: generated code may get faster or slower, though Lyth adds no
  runtime and keeps what code costs visible in the source.

## Go releases

A Lyth release supports Go 1.26 through the Go release it was built with.
`lyth version` prints both:

```text
lyth version X.Y.Z (built with go1.27.3; supports Go 1.26 to 1.27)
```

The newest Go a Lyth binary knows is the one it was built with, because its
`go/types` checks the generated Go and its export data reader reads the types
of Go dependencies. Installing with `go install` builds `lyth` with the Go on
the machine; the Homebrew formula builds it with Homebrew's Go.

A newer Go release usually works, so `lyth` does not refuse one. When a build
fails in a way a newer Go can cause (Go types it cannot read, or generated Go
its `go/types` rejects), the diagnostics name the cause and the fix: build with
a supported Go (`GOTOOLCHAIN=go1.27.0`), or install a `lyth` built with the
newer Go. An older Go than the minimum is an error with the same advice, and
so is a `go.mod` whose `go` line is older than `go 1.26`.

Each Lyth release is tested with the Go of its own `go.mod` and with the
latest Go release. The minimum moves up only in a minor release, and never past
the oldest Go release the Go team still supports (the two latest).

## Evolving a Lyth library

A Lyth package is a Go package, so its API has two audiences: Lyth callers,
who see the Lyth declarations, and Go callers, who see the generated Go. Some
changes are compatible for one and not the other:

| Change | Lyth callers | Go callers |
|---|---|---|
| Add a parameter with a default | compatible | breaking: the Go function gains a parameter |
| Make a result or parameter nullable (`T` to `T?`) | breaking: callers must check | breaking for value types: `T` becomes `*T` |
| Make a function `throws` | breaking | breaking: it gains an `error` result |
| Add a field to a struct | breaking for positional construction `User("a", 1)`; named arguments are fine | compatible for keyed literals |
| Add an entry to an enum or a member to a sealed interface | breaking: every exhaustive `when` without `else` stops compiling | compatible |
| Rename a parameter | breaking for calls that name it | compatible |

A library publishes the Go that `lyth gen -in-tree` writes from its sources;
Lyth callers read the sources and Go callers the Go, so both must come from the
same commit ([publishing a Lyth library](go-interop.md#publishing-a-lyth-library)).
In a minor version of a library, add new functions and fields instead of
changing existing ones, and keep `else` branches in `when`s over another
package's enums if that package may add entries.
