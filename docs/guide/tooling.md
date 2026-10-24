# Tooling

The `lyth` command compiles Lyth to Go and drives the official `go` command for
everything else, so modules, build caching, cross-compilation, the race
detector, profilers, and debuggers work as they do for Go. The architecture is
described in [compiler](../design/compiler.md).

## Installing

Lyth needs Go 1.26 or newer. From a checkout of the repository:

```bash
go install ./cmd/lyth
```

## Commands

| Command | What it does |
|---|---|
| `lyth run file.lyth [-- args]` | Builds and runs a single file, inside or outside a module. Outside a module, Lyth creates a throwaway one in your user cache. |
| `lyth run ./cmd/api [-- args]` | Builds and runs a main package of the current module. |
| `lyth build [-o out] [packages] [-- go build flags]` | Compiles packages with `go build`. With no packages, every directory containing `.lyth` files. |
| `lyth check [packages]` | Parses and type-checks, test files included, without running the Go compiler on the result. The fastest feedback loop. |
| `lyth test [packages] [go test flags]` | Compiles packages with their `_test.lyth` files and runs `go test` ([testing](testing.md)). |
| `lyth vet [packages]` | Runs `go vet` on the generated code and maps findings to `.lyth` positions. |
| `lyth gen [-stdout \| -in-tree] [packages]` | Writes the generated Go: to stdout, or next to the sources as `*.lyth.go` files. |
| `lyth mod tidy` | `go mod tidy` that also sees imports in `.lyth` files ([interop](go-interop.md#modules-and-third-party-packages)). |
| `lyth version` | Prints the compiler version. |

Common flags: `-tags a,b` (build tags), `-diagnostics=json` (one JSON object
per diagnostic, for editors and CI), `-v` (diagnostics include the original Go
toolchain message where one exists), `-no-line-directives`, and `-timings`
(time per compiler phase). For `lyth test`, `-v` is `go test`'s.

## What a build does

`lyth build` never writes into your source tree. It generates Go into `.lyth/gen`
at the module root and builds with `go build -overlay=.lyth/overlay.json`, so
the `go` command sees each `x.lyth` as a Go file `x.lyth.go` in the same
directory. The `.lyth/` directory ignores itself in git. If you want the
generated code committed, for example so that users of a library need no Lyth
toolchain, use `lyth gen -in-tree`.

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
tables the directives produce. Local variables keep their Lyth names.

## Build tags, platforms, and Go flags

Build tags, `GOOS`/`GOARCH`, `CGO_ENABLED`, and `GOFLAGS` come from the
environment as for Go. A file named `net_linux.lyth` is Linux-only, like
`net_linux.go`. Flags after `--` go to `go build`:

```bash
GOOS=linux GOARCH=arm64 lyth build -o app ./cmd/api
lyth build ./cmd/api -- -race -trimpath
```
