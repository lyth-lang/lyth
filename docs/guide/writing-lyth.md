# Writing Lyth

Lyth is a programming language that compiles to readable Go and builds with the `go` command. It
keeps Go's semantics and Go's spelling for everything Go has (`func`, `:=`, `int`, `map[K]V`,
`[T]` generics), and adds what Go lacks: `throws` and `?` instead of `if err != nil`, nullable
types, `if` and `when` as expressions, lambdas, named and default arguments. Every Go package
works with its exact Go names.

This page is for people and for AI coding agents; `lyth guide` prints it for the installed
version. Lyth changes between versions, so when this guide or the compiler disagrees with what
you remember about Lyth, they are right.

## How to work

- A Lyth project is a Go module: `go.mod` at the root, `.lyth` files in package directories,
  `_test.lyth` files beside them. `lyth init` creates one. Imports come from `go.mod` as in Go;
  `lyth mod tidy` adds the modules your `.lyth` files import.
- Run `lyth check` after each change and fix what it reports before writing more. It is fast,
  and test files are included. Diagnostics point at `.lyth` lines and speak Lyth; many end with a
  `hint:` that gives the exact edit.
- `lyth fix` applies the edits that only respell code: older Lyth syntax, and Kotlin and Go
  spellings of Lyth constructs (the [table below](#changes-between-versions)). When a project
  shows many errors after a Lyth upgrade, run `lyth fix` first, then fix the rest by hand.
- `lyth run` runs a program (`lyth run file.lyth`, or a package directory; program arguments go
  after `--`), `lyth test` runs the tests (`go test` flags work), `lyth vet` runs `go vet` on the
  generated code.
- `lyth gen -stdout file.lyth` prints the Go a file becomes. Use it when you are unsure what a
  construct does.
- Keep the work in `.lyth` files. Don't edit the generated Go (in `.lyth/gen`), and don't write
  `.go` files to get around a Lyth error: a package cannot mix Lyth and non-test Go files, and
  Lyth errors have Lyth fixes.
- Go documentation applies as is: `go doc net/http.Server` shows what Lyth calls `http.Server`.

## The language at a glance

```kotlin
import "errors"
import "fmt"
import "strconv"
import "strings"
import "lyth/coll"

enum Role { Admin, Member }

struct User {
    Name: string
    Age: int
    Role: Role
    Email: string?
}

func User.Label(): string = "${Name} (${Age})"

func *User.Birthday() {
    self.Age++
}

struct NotFound {
    Name: string
}

func *NotFound.Error(): string = "no user named ${Name}"

func parseAge(s: string): int throws {
    n := strconv.Atoi(strings.TrimSpace(s)) catch (err) {
        throw err.wrap("age ${s}")
    }
    if n !in 0..150 {
        throw errors.New("age ${n} is out of range")
    }
    return n
}

func find(users: []User, name: string): *User throws {
    for i, u in users {
        if u.Name == name {
            return &users[i]
        }
    }
    throw &NotFound(Name = name)
}

func main() throws {
    users := [
        User(Name = "ada", Age = parseAge("36")?, Role = Role.Admin),
        User(Name = "bob", Age = parseAge(" 41 ")?, Role = Role.Member, Email = "bob@example.com"),
    ]
    ada := find(users, "ada")?
    ada.Birthday()
    fmt.Println(ada.Label())

    for u in users {
        role := when u.Role {
            Admin -> "admin"
            Member -> "member"
        }
        fmt.Println("${u.Name}: ${role}, ${u.Email ?: "no email"}")
    }

    names := users.Filter { it.Age > 40 }.Map { strings.ToUpper(it.Name) }
    fmt.Println(names)

    guest := find(users, "eve") catch (e: *NotFound) {
        fmt.Println(e.Error())
        &User(Name = "guest")
    }?
    fmt.Println(guest.Label())

    age := parseAge("abc") catch (err) {
        fmt.Println(err)
        0
    }
    fmt.Println(age)
}
```

## Declarations

- Functions: `func add(a: int, b: int): int { return a + b }`, or with one expression,
  `func add(a: int, b: int): int = a + b`. Parameters and results are `name: Type` and `: Type`.
  An unexported expression-bodied function may omit its result type (`func sq(n: int) = n * n`).
  A block body returns with `return`; the last expression is not returned by itself.
- Several results: `func split(s: string): (string, string)`, `return a, b`.
- Locals: `x := 1` declares a mutable variable, `x = 2` assigns, `x: int64 = 0` or `x: int64`
  (the zero value) when the type matters. `a, b := f()` and `a, b = b, a + b` work as in Go.
  There is no `val` or `var` inside functions.
- Package level: `var Default = "x"`, `const Max = 10`. Constants are package-level only.
- Parameters, loop variables, `catch` bindings, and `self` are read-only.
- Visibility is Go's: a capitalized name is exported. Write Go-style names yourself: `UserID`,
  `NewServer`, `ID`.
- Structs: `struct User { ... }` with one `Name: Type` field per line. Exported fields get
  lower-camel `json` tags automatically. Construct by calling the type:
  `User(Name = "a", Age = 3)`, positionally with every field, and `&User(...)` for a pointer. Go
  structs take named arguments: `&http.Server(Addr = ":8080")`.
- Methods are declared outside the struct: `func User.Hello(): string` (value receiver),
  `func *User.Rename(n: string)` (pointer receiver). The receiver is `self`; fields are also in
  scope by bare name. A method that changes the receiver's fields needs `*`.
- Interfaces: `interface Shape { Area(): float64 }`. Conformance is structural, as in Go;
  `struct Circle : Shape { R: float64 }` also declares it, and the compiler checks it.
- Generics: `func Max[T: cmp.Ordered](a: T, b: T): T`, `struct Page[T] { Items: []T }`,
  `func Page[T].First(): T`. Calls infer type arguments; write them in brackets when nothing
  determines them: `errors.AsType[*NotFound](err)`.
- Extension functions: `func string.Shout(): string = strings.ToUpper(self) + "!"`.
- Enums: `enum Color { Red, Green }`, used as `Color.Red`, or `Red` inside `when c { ... }`.
  `sealed interface Shape { ... }` is implemented only by structs of the same package that
  declare it.
- Default and named arguments: `func connect(host: string, port: int = 5432)`, called as
  `connect("db", port = 6543)`. Go functions take positional arguments only.

## Types

- Go's names: `int`, `int64`, `float64`, `string`, `bool`, `byte`, `rune`, `any`, `error`,
  `[]T`, `[N]T`, `map[K]V`, `chan T`, `*T`.
- Function types are `(A, B) -> R`, `(A) -> unit`, and `(A) -> R throws` for one that can fail.
- `T?` is nullable. `[]User?` is a slice of nullable users: `?` belongs to the element. Slices
  and maps themselves are never nullable; an empty one is the absent value.
- Numbers never convert implicitly: `float64(n)`, `int64(x)`.
- Literals take their type from context: `xs: []int = []`, `m: map[string]int = [:]`,
  `["a": 1]`. Go's typed literals work too: `[]int64{1, 2}`, `map[string]any{"ok": true}`.

## Errors

- A function that can fail is `func f(): T throws`, Go's `(T, error)`.
- `f()?` returns the error to the caller. `throw err` returns one. `err.wrap("loading ${path}")`
  wraps it with context.
- `f() catch { fallback }`, or `f() catch (err) { ...; fallback }`, handles it in place.
  `f() catch {}` ignores it explicitly.
- `f() catch (e: *NotFound) { ... }` handles one error type (through wrapping); it must end with
  `?` or a final `catch` for the other errors.
- An error that is not handled is a compile error. `func main() throws` prints the error and
  exits with status 1, and tests may be `throws`.
- `n, err := strconv.Atoi(s)` also works, as in Go.

## Null safety

- A `T?` needs a check before member access: `if u != null { u.Name }` (a smart cast), or
  `u?.Name`, `u?.Name ?: "anon"`, `u!!` (panics on null).
- `x := f() ?: return` exits early on null.
- Smart casts apply to parameters, `self`, and locals the function never reassigns. For a field
  or a package-level variable, copy it into a local first.
- Values from Go packages are usable as non-null.

## Control flow

- No parentheses around conditions, braces always: `if c { } else if d { } else { }`,
  `while c { }`, `while true { }`.
- Loops: `for x in xs`, `for i, x in xs`, `for k, v in m`, `for i in 0..<n`, `for i in 1..n`.
  There is no `range` and no three-part `for`.
- `if` and `when` are expressions: `label := if n > 0 { "pos" } else { "neg" }`.
  `when code { 200, 204 -> a; in 500..599 -> b; else -> c }`, and `is T ->` branches over
  interfaces. A `when` over an enum or a
  sealed interface must cover every case or have `else`.
- `x in xs`, `k in m`, `"@" in email`, `code in 200..299`, and `!in`.
- Labels: `outer@ for x in xs { ... break@outer }`. `break` inside `when` exits the loop.
- Lambdas: `{ a, b -> a + b }`, `{ it * 2 }`, and a trailing lambda `xs.Map { it * 2 }`. A loop
  or `if` header cannot end in a trailing lambda: parenthesize the call.
- `go { ... }`, `defer { ... }`, `defer f.Close() catch {}`.
- Channels: `make(chan int, 10)`, `ch.send(v)`, `ch.receive()`, `v, ok := ch.receive()`,
  `ch.close()`, `for v in ch`, and `select { v := ch.receive() -> ...; else -> ... }`.

## Tests

- Tests are Go tests in `_test.lyth` files of the same package (no `package x_test`):
  `func TestParse(t: *testing.T) { ... }`, and a test may be `throws`. `lyth test ./...` runs
  them, with `go test` flags such as `-v` and `-run`.
- `go vet` runs with the tests and requires a constant format string: write
  `t.Errorf("got %d, want %d", got, want)`, or `t.Error("got ${got}, want ${want}")` with a
  template, never a template inside `t.Errorf`.

## Coming from Go

What Go programmers write by habit, and the Lyth for it:

| Go | Lyth |
|---|---|
| `func (u *User) Rename(n string)` | `func *User.Rename(n: string)`, with `self` for the receiver |
| `x int`, `func f() int` | `x: int`, `func f(): int` |
| `Point{X: 1, Y: 2}` | `Point(X = 1, Y = 2)` |
| `for i, x := range xs` | `for i, x in xs` |
| `for i := 0; i < n; i++` | `for i in 0..<n` |
| `for { }`, `for cond { }` | `while true { }`, `while cond { }` |
| `ch <- v`, `<-ch`, `close(ch)` | `ch.send(v)`, `ch.receive()`, `ch.close()` |
| `go func() { ... }()` | `go { ... }` |
| `nil` | `null` |
| `if err != nil { return err }` | `f()?` |
| `[T cmp.Ordered]` | `[T: cmp.Ordered]` |
| `var x = 1` in a function | `x := 1` |
| `switch` | `when` |
| `if v := f(); v > 0 { }` | `v := f()`, then `if v > 0 { }` |
| `strings.FieldsFunc(s, func(r rune) bool { ... })` | `strings.FieldsFunc(s) { r -> ... }` |

## Coming from Kotlin

| Kotlin | Lyth |
|---|---|
| `fun f(x: Int): Int` | `func f(x: int): int` |
| `val x = 1`, `var y = 2` | `x := 1`, `y := 2` |
| `Int`, `String`, `Boolean`, `List<T>`, `Map<K, V>` | `int`, `string`, `bool`, `[]T`, `map[K]V` |
| `fun <T> f()`, `Box<T>` | `func f[T]()`, `Box[T]` |
| `this` | `self` |
| `"$name"` | `"${name}"` |
| `class User(val name: String)` | `struct User { Name: string }` |
| methods inside the class | `func User.Name()` at top level |
| `companion object`, static methods | package functions (`NewUser`) |
| `listOf(1, 2)`, `mapOf("a" to 1)` | `[1, 2]`, `["a": 1]` |
| `when (val x = f())` | `when x := f()` |
| `for (x in xs)` | `for x in xs` |
| `try { } catch (e: E) { }` | `f() catch (e: E) { }` |
| `xs.filter { }.map { }` | `import "lyth/coll"`, then `xs.Filter { }.Map { }` |

## Not supported yet

Struct embedding in Lyth structs, `when` over errors, `defer f()?`, external test packages
(`package x_test`), packages mixing Lyth and non-test Go files, grouped `import ( ... )`, local
`const`, and `T?` on type parameters. The compiler reports each of these with a way around it.

## Changes between versions

Lyth's syntax has changed and will change again. The compiler keeps recognizing older forms and
reports each with the edit to today's form; `lyth fix` applies those edits to a whole module.
The forms below were Lyth before 0.1.0, and most are also what Kotlin and Go habits produce.

| Older form | Write instead | `lyth fix` |
|---|---|---|
| `fun f()` | `func f()` | yes |
| `val x = e`, `var x = e` (in a function) | `x := e` | yes |
| `val x: T = e` | `x: T = e` | yes |
| `val X = e` (package level) | `var X = e` | yes |
| `Int`, `String`, `Boolean`, `Long`, `Double`, ... | `int`, `string`, `bool`, `int64`, `float64`, ... | yes |
| `List<T>`, `Map<K, V>`, `Chan<T>` | `[]T`, `map[K]V`, `chan T` | yes |
| `fun <T> f()`, `struct Box<T>`, `f<T>(x)` | `func f[T]()`, `struct Box[T]`, `f[T](x)` | yes |
| `[T cmp.Ordered]` | `[T: cmp.Ordered]` | yes |
| `this` | `self` | yes |
| `nil` | `null` | yes |
| `"$name"` | `"${name}"` | yes |
| `for (x in xs)` | `for x in xs` | yes |
| `when (val x = e)` | `when x := e` | yes |
| `(a, b) = f()` | `a, b = f()` | yes |
| `Point{X: 1}` | `Point(X = 1)` | yes |
| `strings.split(s, ",")` | `strings.Split(s, ",")` | yes |
| `func (p *T) M()` | `func *T.M()`, and `self` in the body | no |

## Topics

`lyth guide <topic>` prints the documentation of this version:

- `tour` — the language by example
- `go` — Lyth for Go programmers: each feature next to the Go it replaces
- `cheatsheet` — Lyth syntax next to the Go it compiles to
- `interop` — Go packages, modules, and Lyth packages seen from Go
- `testing` — tests, benchmarks, and fuzzing
- `tooling` — the `lyth` command, diagnostics, editors, and builds
- `language` — the language reference
- `types` — the type system: nullability, platform types, generics
- `errors` — error handling in depth
- `collections` — the `lyth/coll` collection functions
