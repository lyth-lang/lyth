<p align="center">
  <img src="website/public/logo.svg" alt="Lyth" width="128">
</p>

# Lyth

**[Documentation](https://lyth.bkymz.net)** · **[AI coding agents](https://lyth.bkymz.net/guide/ai-agents/)** · [Tour](https://lyth.bkymz.net/guide/tour/) · [Lyth for Go programmers](https://lyth.bkymz.net/guide/lyth-for-go-programmers/) · [Cheat sheet](https://lyth.bkymz.net/guide/cheatsheet/)

Lyth is a statically typed language that compiles to readable Go. It takes the
boilerplate out of Go (`if err != nil` blocks, nil-check ladders, verbose
closures, hand-written `json` tags) so programs are shorter and easier to read,
and it keeps everything else: Go's runtime and speed, its standard library,
every Go package, and the official toolchain. Where Go already has a concept,
Lyth spells it Go's way, and what it adds (null safety, `?` for errors, `when`,
lambdas) is a small set of features familiar from modern languages.

> **Built for AI coding agents.** Lyth works with Claude Code, Codex, Cursor, and any agent
> that reads `AGENTS.md`: `lyth init` gives every project one, `lyth guide` teaches the agent
> the exact Lyth version installed, and `lyth check` and `lyth fix` correct what it gets wrong.
> In our evals with Claude Code, that help makes the first build succeed three times in four
> instead of never. [Set it up in a minute](#ai-coding-agents).

> **Status: before 1.0.** The compiler works end to end: it parses,
> type-checks, generates Go, validates it, builds it, and maps errors and stack
> traces back to Lyth source. The syntax can still change between releases
> (`lyth fix` updates code written for an older one), and the sections of the
> [specification](docs/spec/language.md) that are not implemented yet are
> tagged **[planned]**.

```kotlin
import "errors"

struct User {       // json tags: "id", "name", "email"
    ID: int64
    Name: string
    Email: string?  // *string in Go, null in JSON
}

func find(id: int64): User? throws {  // Go: (*User, error)
    if id < 0 {
        throw errors.New("negative id ${id}")
    }
    if id == 0 {
        return null  // absent is a value, not an error
    }
    return User(ID = id, Name = "Ada", Email = null)
}

func describe(raw: string): string throws {
    id := raw.toInt64()?  // ? propagates the parse error
    user := find(id)? ?: return "nobody"
    email := user.Email ?: "no email"
    return "${user.Name} <${email}>"
}

func main() throws {
    println(describe("1")?)
    println(describe("x") catch (err) { "error: ${err}" })
}
```

## Why Lyth

- **Less code for the same program.** `?` propagates an error, `catch` handles
  one inside an expression, `?:` supplies a fallback for a missing value, and
  lambdas, `when`, and string templates replace the statements Go needs around
  them. In the [benchmark suite](docs/design/benchmarks.md), where each workload
  is written independently in both languages, the Lyth versions are 19%
  shorter overall (277 versus 344 lines) and 37% shorter in error-heavy code, and a
  collection pipeline takes less than half the lines of the equivalent loops.
- **Easier to read and review.** Much of today's code is written with AI, often
  hundreds of lines at a time, and people still have to read it. With the
  plumbing gone, what is left is what the program does. The compiler checks
  what a reviewer would otherwise have to spot: using a nullable value without
  a check, or leaving an error unhandled, is a compile error rather than a
  crash in production.
- **Error handling without the ceremony.** `func find(id: int64): User? throws`
  is Go's `func find(id int64) (*User, error)`. Errors stay values, as in Go:
  no exceptions, no unwinding, no hidden control flow. Only the repetition
  around them is gone.
- **All of Go, with no trade-off.** The generated Go is what a careful Go
  programmer would write: no runtime, no reflection, no framework. Across 18
  benchmarks (JSON, HTTP, PostgreSQL, concurrency, CPU- and allocation-heavy
  code), Lyth and hand-written Go show no measurable difference in time,
  allocations, or memory ([results](docs/design/benchmarks.md)). Every Go
  package is used directly under its Go names, Lyth packages are ordinary Go
  packages that Go code can import, and builds use the official `go` command.
- **Built for backends.** [`examples/todoapi`](examples/todoapi) is a complete
  REST API in Lyth: routing, JSON, bearer-token authentication, PostgreSQL
  through `pgx`, structured logging, graceful shutdown, and unit and
  integration tests, on `net/http`, `pgx`, and other ordinary Go libraries
  with no Lyth framework in between.

## AI coding agents

Lyth is built to be written by AI coding agents as well as by people. Models have seen little
Lyth, and what they have seen may describe an older version, so Lyth doesn't rely on what they
remember: the installed compiler teaches the language and corrects mistakes.

```bash
lyth init example.com/app    # a new module with AGENTS.md and CLAUDE.md for agents
lyth guide                   # the guide to writing the installed version of Lyth
lyth check                   # diagnostics with the exact edit (-diagnostics=json for tools)
lyth fix                     # applies every edit that only respells code
```

- **`lyth guide`** prints the guide to writing Lyth, built into the binary, so it always matches
  the compiler: how to work with it, the language at a glance, what Lyth spells differently from
  Go and Kotlin, and the syntax that changed between versions. `lyth guide topics` lists the
  reference pages it also carries.
- **`lyth init`** writes an `AGENTS.md`, read by Codex, Cursor, GitHub Copilot, and other agents,
  and a `CLAUDE.md` that imports it for Claude Code. In an existing module it adds only what is
  missing. Neither holds language rules, only commands, so updating Lyth updates what agents
  learn.
- **`lyth check` and `lyth fix`** turn the Go and Kotlin habits models bring with them, and
  older Lyth syntax, into diagnostics with exact edits; `lyth fix` applies them across a module.
- **Claude Code** also has a plugin whose skill does all this for any Lyth work:

  ```bash
  claude plugin install lyth --marketplace lyth-lang/lyth
  ```

Measured with Claude Code on 12 tasks, three runs each ([`evals/`](evals)):

| | with `AGENTS.md` | with the skill | no help |
|---|---|---|---|
| first build succeeded | 27/36 | 26/36 | 0/36 |
| builds per run | 1.5 | 1.5 | 4.2 |
| turns per run | 6.5 | 8.6 | 14.2 |

The [AI coding agents guide](docs/guide/ai-agents.md) covers setup for each agent, the commands,
and how agents stay current when Lyth changes.

## What Lyth changes, and what it does not

| Lyth adds | Lowers to |
|---|---|
| `throws`, postfix `?`, `catch`, `throw`, typed `catch (e: *NotFound)` | `(T, error)` results, `if err != nil` blocks, `errors.AsType` |
| nullable types `T?`, smart casts, `?.`, `?:`, `!!` | plain pointers/interfaces with nil checks |
| read-only parameters, loop variables, and value receivers | compile-time checks only |
| lambdas `{ x -> x * 2 }`, `it`, trailing lambdas | Go closures |
| `if`/`when` expressions, string templates `"${x}"` | statements, `+` with `strconv` |
| type tests `x is T`, `as`, `as?`, `when x { is T -> }` | type assertions and type switches |
| struct construction `User(Name = "a")`, automatic `json` tags | keyed composite literals, lower-camel `json` keys |
| `type Handler = (Ctx) -> unit throws`, `type UserID int64` | `type Handler = func(Ctx) error`, `type UserID int64` |
| default and named arguments `connect("db", timeout = 5 * time.Second)` | plain positional calls |
| `x in xs`, `code in 200..299`, `for x in xs`, `while c`, `break@outer` | `slices.Contains`, comparisons, `for range` loops, labels |
| declared conformance `struct Circle : Shape` | `var _ Shape = Circle{}` |
| `ch.send(v)`, `ch.receive()`, `ch.close()`, `select { v := ch.receive() -> ... }` | `ch <- v`, `<-ch`, `close(ch)`, Go `select` |
| `enum Color { Red, Green }`, `sealed interface Shape`, exhaustive `when` | `iota` constants with `String()`, an interface with a marker method, a `switch` with a panicking `default` |

Where Lyth passes a Go concept through unchanged, it keeps Go's spelling:
`func`, `:=`, `int` and `string`, `[]T` and `map[K]V`, `chan T`, generics in
square brackets, conditions without parentheses, a capitalized name is
exported, and a Lyth name is its Go name, so stack traces, `go doc`, and Go
callers show what you wrote.

Lyth keeps Go's semantics everywhere. Go packages are used directly with
their exact Go names (`http.ListenAndServe`, `r.URL`). Lyth packages compile to
ordinary Go packages with idiomatic APIs. There is no runtime, no reflection,
and no framework.

## Quick start

Install with Homebrew on macOS or Linux (it installs Go too), or with Go 1.26 or newer:

```bash
brew install lyth-lang/tap/lyth
go install github.com/lyth-lang/lyth/cmd/lyth@latest
```

Then, in a clone of this repository, run `lyth run examples/hello/hello.lyth`.

Inside a Go module, `lyth` works like the `go` command: `lyth build`, `lyth run
./cmd/api`, `lyth test ./...`, `lyth vet`, plus `lyth check` (type-check only) and
`lyth gen -stdout` (print the generated Go). Builds never write into your source
tree. Errors, `go vet` findings, test failures, and panics point at `.lyth`
lines, with Lyth types and suggested fixes.

`lyth init` makes a directory a Lyth module, with an `AGENTS.md` for
[AI coding agents](docs/guide/ai-agents.md).

Start with the [tour](docs/guide/tour.md), or with
[Lyth for Go programmers](docs/guide/lyth-for-go-programmers.md) if you know Go.
[Tooling](docs/guide/tooling.md) covers every command and flag, and
[testing](docs/guide/testing.md) covers `lyth test`.

## Repository layout

```
cmd/lyth/            CLI
internal/source     files and spans
internal/syntax     lexer, AST, parser
internal/diag       diagnostics model and rendering
internal/types      Lyth types, Go type lifting/lowering
internal/golang     Go package loading (go/packages)
internal/check      resolution and type checking
internal/lower      typed AST -> go/ast
internal/emit       printing, source maps, //line directives
internal/build      orchestration, overlays, validation, go toolchain
tests/              end-to-end suites (run, check, gen golden, fix, toolchain, docs, evals)
skills/lyth/        Claude Code skill, which sends agents to `lyth guide`
evals/              measures how well models write Lyth, with and without the skill
examples/           hello, users, todoapi (REST API on PostgreSQL)
website/            documentation site (Starlight), generated from docs/
bench/              Lyth vs hand-written Go benchmark suite (separate module)
docs/               design, specification, ADRs
```

## Documentation

The documentation is published at [lyth.bkymz.net](https://lyth.bkymz.net); its sources are below.

- Guide: [tour](docs/guide/tour.md), [Lyth for Go programmers](docs/guide/lyth-for-go-programmers.md),
  [AI coding agents](docs/guide/ai-agents.md), [writing Lyth](docs/guide/writing-lyth.md) (what `lyth guide` prints),
  [cheat sheet](docs/guide/cheatsheet.md), [Go interop](docs/guide/go-interop.md),
  [testing](docs/guide/testing.md), [tooling](docs/guide/tooling.md)
- [Design overview](docs/design/overview.md): goals, principles, decision summary
- Specification: [language](docs/spec/language.md), [types](docs/spec/types.md), [errors](docs/spec/errors.md)
- Design: [compiler](docs/design/compiler.md), [interop](docs/design/interop.md),
  [diagnostics](docs/design/diagnostics.md), [collections](docs/design/collections.md),
  [benchmarks](docs/design/benchmarks.md)
- [Architecture decision records](docs/adr/README.md)
- [Releasing](RELEASING.md): where Lyth and its editor extensions are published, and how to release them

The documentation site in [website/](website) is generated from these files;
see [website/README.md](website/README.md).

## Development

```bash
go test ./...                                  # unit + end-to-end tests (needs the go toolchain)
go test ./tests -run TestGenGolden -update     # refresh golden files after intended codegen changes
go test ./internal/syntax -fuzz FuzzParse      # fuzz the parser
go test ./internal/check -fuzz FuzzCheck       # fuzz the checker
```

## License

Lyth is licensed under either of

- the [Apache License, Version 2.0](LICENSE-APACHE), or
- the [MIT license](LICENSE-MIT),

at your option. Unless you explicitly state otherwise, any contribution
intentionally submitted for inclusion in Lyth by you, as defined in the
Apache-2.0 license, shall be dual licensed as above, without any additional
terms or conditions.
