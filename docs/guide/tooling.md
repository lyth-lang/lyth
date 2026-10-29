# Tooling

The `lyth` command compiles Lyth to Go and drives the official `go` command for
everything else, so modules, build caching, cross-compilation, the race
detector, profilers, and debuggers work as they do for Go. The architecture is
described in [compiler](../design/compiler.md).

## Installing

Lyth needs Go 1.26 or newer, which it uses to build programs. With Homebrew, on macOS or
Linux (it installs Go too; `brew upgrade lyth` updates):

```bash
brew install lyth-lang/tap/lyth
```

With Go, or from a checkout of the repository:

```bash
go install github.com/lyth-lang/lyth/cmd/lyth@latest
go install ./cmd/lyth
```

`lyth version` prints the installed version.

## Commands

| Command | What it does |
|---|---|
| `lyth run file.lyth [-- args]` | Builds and runs that file alone, as `go run file.go` does. Outside a module, Lyth creates a throwaway one in your user cache; inside a module, the file's package is not built with it (`lyth run ./dir` runs the package). |
| `lyth run ./cmd/api [-- args]` | Builds and runs a main package of the current module. `-race` and `-ldflags` go to the Go build; arguments after the package go to the program. |
| `lyth build [-o out] [-race] [packages] [-- go build flags]` | Compiles packages with `go build`. With no packages, every directory containing `.lyth` files. `-o dir/` writes the binary into `dir`. |
| `lyth check [packages]` | Parses and type-checks, test files included, without running the Go compiler on the result. The fastest feedback loop. |
| `lyth test [packages] [go test flags]` | Compiles packages with their `_test.lyth` files and runs `go test` ([testing](testing.md)). |
| `lyth vet [packages]` | Runs `go vet` on the generated code and maps findings to `.lyth` positions. |
| `lyth cover -func=c.out` | Prints the coverage of each function in a profile written by `lyth test -coverprofile` ([testing](testing.md#coverage)). Other modes, such as `-html=c.out`, run `go tool cover`. |
| `lyth gen [-stdout \| -in-tree] [packages \| file.lyth]` | Writes the generated Go: to stdout, or next to the sources as `*.lyth.go` files. `-stdout file.lyth` prints that file's Go only. |
| `lyth fix [-lang X.Y] [packages]` | Rewrites older Lyth syntax, and Go and Kotlin spellings of Lyth constructs, in the packages' files, test files included, then reports the errors left ([below](#lyth-fix)). `-lang` first raises the Lyth version `go.mod` declares. |
| `lyth fmt [-l] [-d] [paths]` | Formats `.lyth` files in place in Lyth's canonical layout ([below](#lyth-fmt)), the current directory and those below it by default; `-l` lists the files that differ and `-d` prints the changes, without writing; `-stdin` formats standard input. |
| `lyth generate [-run re] [-skip re] [-n] [-v] [-x] [packages]` | Runs the `//go:generate` lines of `.lyth` files, test files included, as `go generate` does for `.go` files (which is all it reads): in the package directory, with `$GOFILE`, `$GOLINE`, `$GOPACKAGE`, `$GOOS`, `$GOARCH`, and `$DOLLAR`, quoted arguments, and `-command` aliases. Run `go generate` for the directives in `.go` files. |
| `lyth mod tidy` | `go mod tidy` that also sees imports in `.lyth` files ([interop](go-interop.md#modules-and-third-party-packages)). |
| `lyth mod vendor` | `go mod vendor` that also vendors the packages `.lyth` files import. A module that imports `lyth/coll` cannot build from `vendor/` yet; build it with `GOFLAGS=-mod=mod`. |
| `lyth init [module-path]` | Makes the current directory a Lyth module: `go.mod` (through `go mod init`; the path defaults to the directory's name), `main.lyth` when there is no Lyth code yet, `AGENTS.md` and `CLAUDE.md` for coding agents, and `.lyth/` in `.gitignore`. `go.mod` gets a `//lyth:version` line with the installed version ([compatibility](compatibility.md#the-lyth-version-of-a-module)). In an existing module it adds only what is missing. |
| `lyth guide [topics \| topic]` | Prints the guide to writing this version of Lyth ([writing Lyth](writing-lyth.md)), the list of topics, or one documentation page. |
| `lyth lsp [-no-vet]` | Runs the language server on stdin and stdout, for editors ([editors](#editors)). |
| `lyth version` | Prints the compiler version, the Go it was built with, and the Go releases it supports ([Go releases](compatibility.md#go-releases)). |

Common flags: `-tags a,b` (build tags), `-diagnostics=json` (one JSON object
per diagnostic, for editors and CI), `-v` (diagnostics include the original Go
toolchain message where one exists), `-no-line-directives`, and `-timings`
(time per compiler phase). For `lyth test`, `-v` is `go test`'s.

## Editors

`lyth lsp` is Lyth's language server. It answers from the compiler itself:
every edit re-checks the file's package, with unsaved changes, and
publishes the same diagnostics as `lyth check`, including the in-process
check of the generated Go. Saving a file also runs `go vet`, mapped to `.lyth`
lines (`-no-vet` turns that off). Beyond diagnostics it offers:

- **Hover:** the declaration as written, its doc comment, and the Go it
  compiles to. A Go declaration shows its doc from the Go source.
- **Navigation:** go to definition, including into the Go standard
  library and modules, type definition, references, and highlights. These
  follow what each name resolves to, not its text.
- **Rename** across the module.
- **Completion:** members after `.` (fields, methods, `lyth/coll`'s
  extension functions, a package's exports), the names in scope, and
  import paths.
- **Signature help.**
- **Inlay hints** for inferred types: `x := f()`, loop variables, lambda
  parameters, and results of `func f() = ...`.
- **Quick fixes** from the compiler's suggested edits.
- **Document and workspace symbols.**
- **The generated Go** of a file (the `lyth.showGo` command).

Editor support lives in the repository's `editors/` folder:

- **VS Code** (`editors/vscode-lyth`): highlighting, the language server, ▶
  Run and Run Test buttons, `lyth` tasks, snippets, and a live **Show
  Generated Go** view.
- **Kuana** (`editors/kuana-lyth`): highlighting, outline, run and test
  buttons with test results, and the language server. Kuana's Install button
  installs `lyth` with `go install`.
- **Zed** (`editors/zed-lyth`): the same highlighting, outline, run and
  test buttons, tasks, and snippets as Kuana, and the language server, which
  it finds as `lyth` on your `PATH`. Install it with **zed: install dev
  extension**.
- **Other editors:** the tree-sitter grammar (`editors/tree-sitter-lyth`)
  covers highlighting, outline, indentation, and text objects. For the
  language server, start `lyth lsp` for `.lyth` files, with `go.mod` as the
  root marker. In Neovim, for example:

```lua
vim.filetype.add({ extension = { lyth = "lyth" } })
vim.lsp.config("lyth", { cmd = { "lyth", "lsp" }, filetypes = { "lyth" }, root_markers = { "go.mod" } })
vim.lsp.enable("lyth")
```

## lyth fmt

`lyth fmt` gives Lyth code one layout, as `gofmt` does for Go: four spaces of
indentation per level, one space around `:=`, `=`, comparisons, `&&`, `->`,
and `?:` and after commas and the colon of `name: Type`, none inside
parentheses and brackets or around `.`, `?.`, and ranges (`0..<n`), and
spaces inside the braces of blocks and lambdas (`{ it * 2 }`) but not inside a
typed literal's (`[]int{1, 2}`). Arithmetic keeps the spacing you chose, so
`a*b + c` can show precedence, as gofmt writes it. Trailing comments and
struct tags of consecutive lines are aligned in a column, and runs of blank
lines become one.

Lyth is newline-sensitive, so `lyth fmt` changes only spaces inside lines,
indentation, and blank lines: it never moves a token to another line. It
checks that the formatted file parses to the same syntax tree, and leaves a
file with syntax errors as it is. Editors format through the language server
(**Format Document** in VS Code); in CI, `test -z "$(lyth fmt -l)"` fails
when a file is not formatted.

## What a build does

`lyth build` never writes into your source tree. It generates Go into `.lyth/gen`
at the module root and builds with `go build -overlay=.lyth/overlay.json`, so
the `go` command sees each `x.lyth` as a Go file `x.lyth.go` in the same
directory. The `.lyth/` directory ignores itself in git. The one exception is
`lyth test` with coverage flags: Go's cover tool ignores the overlay, so the
generated files sit beside their sources until `go test` exits
([coverage](testing.md#coverage)). If you want the
generated code committed, for example so that users of a library need no Lyth
toolchain, use `lyth gen -in-tree`. Code that imports `lyth/coll` then imports
its generated copy, which `-in-tree` writes to `internal/lyth/coll` in the
module, so plain `go build` and `go test` work.

```bash
lyth build -timings ./cmd/api
```

```text
lyth: setup                          10.7ms
lyth: parse                           0.3ms
lyth: load Go packages (cached)       2.1ms
lyth: check                           0.3ms
lyth: lower+emit                      1.3ms
lyth: validate                        0.3ms
lyth: write                           1.3ms
lyth: go build                       57.3ms
lyth: total                          73.6ms
```

Most of a build is the Go toolchain. Type information for Go dependencies comes
from `go list -export` and is cached in `.lyth/cache` keyed by the toolchain,
`go.mod`/`go.sum`, build tags, and local package contents; `LYTH_NO_CACHE=1`
turns the cache off. [Benchmarks](../design/benchmarks.md) compares build times
with plain Go.

## Diagnostics

```text
users.lyth:13:18: error[E0202]: mismatched types float64 and int in *
   |
13 |     return price * quantity
   |            ----- ^ -------- int
   |            float64
   |
   = hint: Lyth never converts numbers implicitly; write float64(quantity)
```

Diagnostics use Lyth names and Lyth types, with codes, labels, notes, and
suggested edits. After Lyth's own checker, the generated Go is type-checked
in-process with `go/types`; anything it finds (a gap in Lyth's checker) is
reported at the exact Lyth span with code E0902, never as a raw Go error. The
design is in [diagnostics](../design/diagnostics.md).

### `lyth fix`

Some diagnostics carry a fix: an edit that only respells the code, such as `fun` to `func`,
`val x = 1` to `x := 1`, `Int` to `int`, `Point{X: 1}` to `Point(X = 1)`, or
`strings.split` to `strings.Split` (the full list is in
[writing Lyth](writing-lyth.md#changes-between-versions)). `lyth check` counts them, and
`lyth fix` applies them, compiling again until none is left. Other hints, such as adding `?` to
propagate an error or converting a number, change what the code does, and stay suggestions.
With `-diagnostics=json`, an edit that is a fix has `"fix": true`.

When a Lyth release changes its syntax, the compiler keeps reporting the older form with a fix,
so `lyth fix` brings a module up to date after an upgrade.

`lyth fix -lang=X.Y` also raises the Lyth version the module's `go.mod` declares (its
`//lyth:version` line) to `X.Y` before applying the fixes, so changes that newer version brings
apply to the module ([compatibility](compatibility.md#the-lyth-version-of-a-module)). It only
raises the version, and only up to the installed `lyth`'s.

## AI coding agents

Coding agents learn Lyth from the installed compiler. `lyth guide` prints
[writing Lyth](writing-lyth.md) for the installed version, and `lyth guide <topic>` the other
pages of this documentation. The `AGENTS.md` that `lyth init` writes (and its `CLAUDE.md`,
which imports it for Claude Code) only tells agents to read `lyth guide` and to check their work
with `lyth check` and `lyth fix`; it holds no language rules, so it stays true when Lyth
changes. For Claude Code there is also a skill, installed as a plugin from the repository:

```bash
claude plugin install lyth --marketplace lyth-lang/lyth
```

## Debugging and profiling

Generated code carries `//line` directives, so Go's runtime reports `.lyth`
files and lines:

```text
panic: runtime error: index out of range [10] with length 3

goroutine 1 [running]:
main.pick(...)
	/home/ada/app/main.lyth:2
main.main()
	/home/ada/app/main.lyth:7 +0x1d
```

The same goes for `go vet` findings, race reports, `pprof` profiles, test
failures, and breakpoints in delve, which resolves Lyth lines through the line
tables the directives produce. Local variables keep their Lyth names. Builds put
a directive wherever the Lyth line changes, so every statement has its exact
line; `lyth gen` writes them only before lines that can fail at run time, which
is easier to read. Coverage profiles get Lyth positions another way: `lyth test`
rewrites them through the source map ([coverage](testing.md#coverage)).

## Build tags, platforms, and Go flags

Build tags, `GOOS`/`GOARCH`, `CGO_ENABLED`, and `GOFLAGS` come from the
environment as for Go. A file named `net_linux.lyth` is Linux-only, like
`net_linux.go`. Flags after `--` go to `go build`:

```bash
GOOS=linux GOARCH=arm64 lyth build -o app ./cmd/api
lyth build ./cmd/api -- -race -trimpath
```
