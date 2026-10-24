# ADR 0030: The standard library is compiled with the program

**Status:** Accepted

## Context
Lyth's design keeps collection operations and similar helpers out of the
language and in a library. Such a library is
written in Lyth and must be importable from any Lyth program, including a
single file run with `lyth run file.lyth`, without the user publishing,
fetching, or vendoring a module. The project's module path is a placeholder,
so nothing can be fetched through the module proxy yet.

## Decision
- Standard-library packages are imported as `lyth/<name>`, starting with
  `lyth/coll`. The import path is the same in Lyth and in the generated Go.
- Their Lyth sources are embedded in the `lyth` binary. When a program imports
  one, `lyth` writes the library to a directory of the user cache named after
  its content, and compiles its packages like the program's own: checked,
  lowered, and passed to the go command through the overlay.
- The go command finds the module `lyth` through an alternate go.mod,
  `.lyth/go.mod`, which is the module's go.mod plus `require lyth v0.0.0` and a
  `replace` with the cache directory, passed as `-modfile`. The module's own
  go.mod is never edited. A module whose go.mod already requires `lyth` uses it
  as it is.
- `lyth gen -stdout` and `lyth gen -in-tree` leave the library out, and
  `lyth mod tidy` ignores `lyth/...` imports.

## Alternatives
- **A separate Go module** that programs `go get`: the usual Go route, but it
  needs a fetchable module path and a version for every program, including
  single files.
- **Copying the library into each module** (an `internal/` package placed
  through the overlay): needs no go.mod changes, but `go vet` and `go test`
  require the package directory to exist on disk, which would write into the
  user's tree.
- **Compiler intrinsics for collection operations**: no library at all, but
  special cases in the compiler instead of ordinary Lyth code users can read
  and imitate.

## Consequences
Programs import the library without setup, and the generated Go reads
`coll.Filter(todos, ...)`. Generated Go that imports `lyth/...` needs `lyth` to
build, or a go.mod that requires and replaces `lyth` itself, until the library
is published under a fetchable path. The library's code appears in the
program's build like any dependency and costs what its functions cost: each
`lyth/coll` operation allocates its result.
