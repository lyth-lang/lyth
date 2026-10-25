# Lyth — Design Overview

Lyth is a statically typed language that compiles to readable Go source code,
which the official Go toolchain then builds. It keeps Go's runtime, performance
model, standard library, and ecosystem, and changes how much code a program
takes, how it reads, and how often it fails at runtime: all of Go, in fewer
lines. Where Go has a concept, Lyth spells it Go's way.

This document states the vision, the principles used to make decisions, the goals
and non-goals, and a summary of the major decisions. Normative details live in
the [language spec](../spec/language.md), [type system](../spec/types.md), and
[error semantics](../spec/errors.md). Architecture lives in
[compiler.md](compiler.md), [interop.md](interop.md), and
[diagnostics.md](diagnostics.md).

## 1. What problem Lyth solves

Go's runtime is excellent and its source language is deliberately austere. The
cost of that austerity is length: the same logic takes more lines, and the lines
that carry it are spread between error checks and nil checks. That cost grows as
more code is written with AI and read by people, who review hundreds of lines at
a time and need to see what a program does. For backend developers it
concentrates in a few places:

| Pain point in Go                                    | Lyth answer                                                | Cost to Go-ness |
|-----------------------------------------------------|-----------------------------------------------------------|-----------------|
| `if err != nil { return ..., err }` everywhere      | `throws` functions, postfix `?`, `catch`, `throw`          | none: generates the same code |
| nil pointer / nil interface panics                  | nullable types `T?`, smart casts, `?.`, `?:`, `!!`        | none at runtime |
| verbose closures `func(x int) int { return x*2 }`   | lambdas `{ x -> x * 2 }`, `it`, trailing-lambda syntax     | none: same closures |
| statements where expressions are wanted             | `if`/`when` expressions                                    | none: lowered to statements |
| `fmt.Sprintf` for every message                     | string templates `"Hello, ${user.Name}"`                   | none: lowered to `+`/`strconv` |
| silent mutation bugs (value receivers, aliasing)    | read-only parameters, loop variables, and value receivers  | none: compile-time only |
| JSON tag boilerplate                                | automatic lower-camel `json` tags on exported fields      | small, documented |
| hand-written fan-out/cancellation plumbing          | fallible lambdas make `errgroup`-style libraries concise   | none: plain Go libraries |

Each row is a source-level transformation with no runtime component. Wherever an
idea needs runtime support, a reflection pass, or a framework, it goes into an
optional library or gets dropped.

## 2. Design principles

These principles decide the hard cases. When two of them conflict, the earlier one
wins.

1. **Go semantics are the floor.** Every Lyth program means exactly what its
   generated Go program means. Lyth adds compile-time checks and syntax; it never
   changes the dynamic behavior of Go constructs or imported APIs.
2. **Interop is not a feature, it is the platform.** Any exported Go API must be
   usable from Lyth without wrappers. When Lyth's model and Go's disagree (nullability,
   multiple results, naming), Lyth adapts its view, never the Go API.
3. **What you write is close to what runs.** A Go developer reading generated
   code should recognize the program. No hidden allocations, reflection,
   goroutines, or interface conversions beyond what the source visibly implies.
4. **Explicit over implicit at boundaries; concise inside.** Fallibility,
   visibility, and nullability are declared in signatures, and what a function
   receives is protected: parameters are read-only, and a value receiver is a
   copy that cannot be assigned. Within a body, locals are mutable as in Go, and
   inference and smart casts remove repetition.
5. **One way to say it.** A construct earns a second spelling only for a concrete
   compatibility reason (e.g. raw tuple results for implementing `io.Reader`).
6. **Diagnostics are part of the language.** A feature is not done until its
   misuse produces a precise, actionable error at the right source span.
7. **Small core, libraries for the rest.** HTTP, validation, concurrency helpers,
   and collection utilities are libraries. The language provides the primitives
   (lambdas, fallible function types, generics) that make such libraries pleasant.
8. **Go's spelling for what Go already has; Lyth syntax only for what improves
   on Go.** A Lyth spelling must name something Go lacks, or change a meaning Go
   would get wrong; it is never a different word for the same thing. Types are
   `int`, `map[K]V`, and `chan T`, functions are `func`, and locals are declared
   with `:=`, as in Go; `throws`, `T?`, and `when` are Lyth's because Go has no
   equivalent. `for x in xs` stays instead of `range` because it binds the
   element, not the index: the same word with another meaning would be a false
   friend ([ADR 0031](../adr/0031-go-spelling.md)).

## 3. Goals

1. Static typing with useful compile-time diagnostics.
2. Go's spelling for what Go has (`func`, `:=`, `int`, `map[K]V`, `[T: C]`),
   and a small, familiar syntax for what Lyth adds: `name: Type`, `T?`, `throws`,
   lambdas, `if`/`when` expressions, string templates.
3. Direct compilation to idiomatic, gofmt-clean Go source.
4. Practical, honest interoperability with Go packages (see §5).
5. No changes or wrappers required for existing Go libraries.
6. Runtime performance indistinguishable from idiomatic Go for equivalent code.
7. Diagnostics in terms of Lyth source and Lyth types, including errors that
   originate in the Go toolchain.
8. A small, coherent core with an architecture that can grow into a production
   compiler (incremental builds, LSP, debugger support).

## 4. Non-goals

- **A new runtime, GC, scheduler, or mandatory framework.** Lyth emits plain Go.
- **Exceptions.** `throw` returns an error value one level up; there is no
  unwinding. Panics remain Go panics.
- **Deep immutability / const-correctness.** A read-only parameter or value
  receiver protects the binding's value, not objects reachable through pointers
  (Go has no way to enforce the latter).
- **Implicit numeric conversions.** Kept out on purpose: Go's explicitness here
  prevents real bugs. Lyth compensates with precise diagnostics and fix-its.
- **Operator overloading, macros, compile-time code execution.** These would
  make generated code unpredictable, and compiling untrusted code would stop
  being safe.
- **Renaming Go APIs.** Go identifiers are used verbatim (see ADR 0010).
- **Source compatibility with other languages.** Lyth borrows ideas, not
  source code: a Kotlin or Swift file is not a Lyth file.

## 5. What "full Go compatibility" means

Precisely, and only what has been demonstrated (tracked in
[interop.md §10](interop.md#10-compatibility-matrix)):

- **Calling**: every exported function, method, field, constant, variable, and
  type of any Go package that builds with the user's toolchain is usable from Lyth,
  with types lifted faithfully from `go/types` (no simplified model).
- **Being called**: Lyth packages compile to ordinary Go packages. Go code can
  import them, and their exported API has idiomatic Go signatures
  (`func Find(id int64) (*User, error)`).
- **Toolchain**: `go build/test/vet`, modules, build tags, cross-compilation,
  `-race`, pprof, and delve all work on Lyth output because it is plain Go.
- **Documented limitations** (v0): defining new generic types with type-set
  constraints, cgo inside Lyth files, struct embedding in Lyth-declared structs,
  Go labels/goto, and mixing Go and Lyth files in a single package. Each has a
  specific diagnostic today.

## 6. A taste of Lyth

```kotlin
package users

import "context"
import "database/sql"
import "errors"

struct User {
    ID: int64
    Name: string
    Email: string?            // nullable: *string in Go, null in JSON
}

struct Repo {
    Db: *sql.DB
}

// Fallible: compiles to func (r *Repo) Find(ctx context.Context, id int64) (*User, error)
func *Repo.Find(ctx: context.Context, id: int64): User? throws {
    u := User()
    row := Db.QueryRowContext(ctx, "select id, name, email from users where id = $1", id)
    err := row.Scan(&u.ID, &u.Name, &u.Email)
    if errors.Is(err, sql.ErrNoRows) {
        return null                     // missing user: a value, not an error
    }
    if err != null {
        throw err                       // connection failure: an error
    }
    return u
}

func Greeting(repo: *Repo, ctx: context.Context, id: int64): string throws {
    user := repo.Find(ctx, id)? ?: return "nobody"   // ? = error, ?: = null
    return "Hello, ${user.Name}"
}
```

## 7. Decision summary

| Area | Decision | Record |
|---|---|---|
| Target | Generate Go source via `go/ast` + `go/printer`; build with the official toolchain | [ADR 0001](../adr/0001-compile-to-go-source.md) |
| Implementation | Compiler written in Go; only extra dependency `golang.org/x/tools/go/packages` | [ADR 0002](../adr/0002-implementation-language-and-deps.md) |
| Go types | Load imported packages with `go/packages` (export data); lift `go/types` faithfully | [ADR 0003](../adr/0003-go-type-information.md) |
| Checking | Lyth checker first; in-process `go/types` validation of generated code as safety net | [ADR 0004](../adr/0004-two-level-type-checking.md) |
| Source maps | Exact node-level map (print → reparse → lockstep) plus column-less `//line` directives | [ADR 0005](../adr/0005-source-maps-and-line-directives.md) |
| Build layout | Generated files in `.lyth/gen`, injected with `go -overlay`; `lyth gen --in-tree` for publishing | [ADR 0006](../adr/0006-overlay-builds.md) |
| Bindings | Locals `x := e` and `name: Type = e`, mutable as in Go; package-level `var` and `const`; parameters, loop variables, and value receivers read-only | [ADR 0031](../adr/0031-go-spelling.md) (supersedes [0007](../adr/0007-val-var-and-immutability.md) for locals) |
| Errors | Errors stay Go `error` values; `throws`, postfix `?`, `catch`, `throw`; no exceptions | [ADR 0008](../adr/0008-error-handling.md) |
| Nullability | `T?` with smart casts; platform types for Go values; value optionals boxed as `*T` | [ADR 0009](../adr/0009-nullability.md) |
| Naming | Go identifiers verbatim; capitalization is visibility; a Lyth name is its Go name | [ADR 0022](../adr/0022-capitalization-is-visibility.md) (supersedes [0010](../adr/0010-naming-and-visibility.md)) |
| Types syntax | Go's type names (`int`, `string`, `error`, `any`); `[]T`, `map[K]V`, `chan T`, `*T`, `[N]T`, `(A) -> R`; `?` binds loosest, except that it belongs to the element of a slice, array, map, or channel | [ADR 0011](../adr/0011-type-syntax.md), [ADR 0023](../adr/0023-slice-types.md), [ADR 0031](../adr/0031-go-spelling.md) |
| Spelling | Go's spelling for what Go has: `func`, `:=`, `[T: C]` and `f[T](x)`, unparenthesized headers with block bodies, methods `func T.M()` with the receiver `self`; `for x in xs` instead of `range` | [ADR 0031](../adr/0031-go-spelling.md) |
| Channels | Methods `send`/`receive`/`close`; `select` with `when`-style branches | [ADR 0024](../adr/0024-channel-operations.md) |
| Enums, sealed | `enum Color { Red }` as `iota` constants; `sealed interface`; exhaustive `when` | [ADR 0025](../adr/0025-enums-and-sealed-interfaces.md) |
| Rejected | inferred fallibility, implicit numeric widening, `parallel for` syntax, lowerCamel aliases for Go APIs, receiver-lambda DSLs; `val`/`var` locals, untyped parameters, `range` | [ADR 0012](../adr/0012-rejected-features.md), [ADR 0031](../adr/0031-go-spelling.md) |
| Structs | Capitalized fields are exported; automatic lower-camel `json` tags | [ADR 0013](../adr/0013-struct-fields-and-json.md), [ADR 0022](../adr/0022-capitalization-is-visibility.md) |

## 8. Status

Sections of the [specification](../spec/language.md) that are designed but not
implemented yet are tagged **[planned]**. Every implemented feature is backed by a test
under `internal/**/testdata` or `tests/`.
