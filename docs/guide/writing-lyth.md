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
  `lyth mod tidy` adds the modules your `.lyth` files import (`lyth mod vendor` vendors them).
- Run `lyth check` after each change and fix what it reports before writing more. It is fast, and
  test files are included. `lyth fmt` lays the files out canonically. Diagnostics point at `.lyth`
  lines and speak Lyth; many end with a `hint:` that gives the exact edit.
- `lyth fix` applies the edits that only respell code: older Lyth syntax, and Kotlin and Go
  spellings of Lyth constructs (the [table below](#changes-between-versions)). When a project
  shows many errors after a Lyth upgrade, run `lyth fix` first, then fix the rest by hand.
- `lyth run` runs a program (`lyth run file.lyth`, or a package directory; program arguments go
  after `--`), `lyth test` runs the tests (`go test` flags work), `lyth vet` runs `go vet` on the
  generated code. A profile from `lyth test -coverprofile=c.out` names `.lyth` files: read it
  with `lyth cover -func=c.out` (`go tool cover -func` cannot parse Lyth).
- `lyth gen -stdout file.lyth` prints the Go a file becomes. Use it when you are unsure what a
  construct does.
- `lyth generate ./...` runs the `//go:generate` lines of `.lyth` files (`go generate` reads only
  `.go` files).
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

func User.Label(): string = "${self.Name} (${self.Age})"

func *User.Birthday() {
    self.Age++
}

struct NotFound {
    Name: string
}

func *NotFound.Error(): string = "no user named ${self.Name}"

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

- Imports: `import "fmt"`, or a group with one per line: `import ( "fmt"; h "net/http" )`
  written over several lines. Rename as in Go: `import h "net/http"`, `import _ "path"`.
- Functions: `func add(a: int, b: int): int { return a + b }`, or with one expression,
  `func add(a: int, b: int): int = a + b`. Parameters and results are `name: Type` and `: Type`.
  An unexported expression-bodied function may omit its result type (`func sq(n: int) = n * n`).
  A block body returns with `return`; the last expression is not returned by itself.
- Several results: `func split(s: string): (string, string)`, `return a, b`. Results may be
  named as in Go, `func split(n: int): (q: int, r: int)` (a bare `return` returns them), and with
  `throws`, `func *W.Write(p: []byte): (n: int) throws`: `defer` blocks can set `n`.
- Locals: `x := 1` declares a mutable variable, `x = 2` assigns, `x: int64 = 0` or `x: int64`
  (the zero value) when the type matters. `a, b := f()` and `a, b = b, a + b` work as in Go.
  There is no `val` or `var` inside functions.
- Package level: `var Default = "x"`, `var mu: sync.Mutex` (the zero value), `const Max = 10`.
  `const` also works inside a function. Groups are Go's, with `name: Type` entries:
  `const ( FlagA: uint = 1 << iota; FlagB )` and `var ( ErrA = errors.New("a"); count: int )`,
  one per line; an entry with only a name repeats the one above with the next `iota`.
  `func init() { ... }` runs before `main`, as in Go; a package may have several.
- Files of the package directory are embedded as in Go: `//go:embed schema.sql` on the line
  before `var schema: string` (or `[]byte`, or `embed.FS` for several files), with no
  initializer. `//go:noinline` and the other compiler directives go on the line before a `func`.
- Parameters, loop variables, `catch` bindings, and `self` are read-only. `:=` may replace a
  parameter with a value of its type derived from it, as Go code does:
  `ctx, cancel := context.WithTimeout(ctx, d)`, `g, ctx := errgroup.WithContext(ctx)`,
  `name := strings.TrimSpace(name)`.
- Visibility is Go's: a capitalized name is exported. Write Go-style names yourself: `UserID`,
  `NewServer`, `ID`.
- Structs: `struct User { ... }` with one `Name: Type` field per line. A struct may also be
  declared inside a function, for table tests and request bodies (`struct tc { in: string; want: int }`);
  it has no methods. Exported fields get
  lower-camel `json` tags automatically (`UserID` → `userID`, `IDs` → `ids`, `HTTPServer` →
  `httpServer`), and a null `T?` field is left out of the JSON. A name like `ETag` warns
  (W0008): give it a tag such as `json:"etag"`. `json:",string"` keeps the automatic name, and
  `json:""` keeps Go's: the field is untagged in Go and encodes as `"ETag"`.
  Construct by calling the type:
  `User(Name = "a", Age = 3)`, positionally with every field, and `&User(...)` for a pointer. Go
  structs take named arguments: `&http.Server(Addr = ":8080")`.
- Methods are declared outside the struct: `func User.Hello(): string` (value receiver),
  `func *User.Rename(n: string)` (pointer receiver). The receiver is `self`, and fields and
  methods are reached only through it: `self.Name`, `self.validate()`. A bare `Name` in a method
  is an error, as in Go. A method that changes the receiver's fields needs `*`.
- Embedding is Go's: a type alone on a line of a struct (`http.ResponseWriter`, `sync.Mutex`,
  `pb.UnimplementedGreeterServer`, `Base`). Promoted members are reached through the value or
  `self` (`self.Lock()`); the field is named after the type (`self.ResponseWriter`). An embedded
  struct is flattened into JSON. `Store?` embeds an interface that may stay null (partial fakes).
- Interfaces: `interface Shape { Area(): float64 }`. Conformance is structural, as in Go;
  `struct Circle : Shape { R: float64 }` also declares it, and the compiler checks it.
- Generics: `func Max[T: cmp.Ordered](a: T, b: T): T`, `struct Page[T] { Items: []T }`,
  `func Page[T].First(): T`. Calls infer type arguments; write them in brackets when nothing
  determines them: `errors.AsType[*NotFound](err)`. Type sets are Go's: `[T: int | float64]`,
  `interface Number { ~int | ~float64 }`. `T?` on a type parameter is not supported: return
  `(T, bool)` as Go does.
- Extension functions: `func string.Shout(): string = strings.ToUpper(self) + "!"`.
- Defined types and aliases, as in Go: `type UserID int64`, `type Role string`,
  `type Set[T: comparable] map[T]struct{}` (a new type: `UserID(n)` converts, and methods can
  be declared on it), and `type Handler = (http.ResponseWriter, *http.Request) -> unit throws`
  (another name for the same type; generic too: `type Set[T: comparable] = map[T]bool`).
  Function types may name their parameters: `(name: string, depth: int) -> bool`. Structs and
  interfaces keep `struct Name { ... }` and `interface Name { ... }`.
- Enums: `enum Color { Red, Green }`, used as `Color.Red`, or `Red` inside `when c { ... }`.
  JSON carries the entry name in lower camel case (`"red"`) and rejects anything else. For
  fixed wire values, use a string-backed enum: `enum Plan: string { Free = "free", Pro = "pro" }`
  (Go's `type Plan string`; pgx and `database/sql` store it as text). A string-backed enum has
  no zero value unless an entry is `""`.
  `sealed interface Shape { ... }` is implemented only by structs of the same package that
  declare it.
- Default and named arguments: `func connect(host: string, port: int = 5432)`, called as
  `connect("db", port = 6543)`. Go functions take positional arguments only.

## Types

- Go's names: `int`, `int64`, `float64`, `string`, `bool`, `byte`, `rune`, `any`, `error`,
  `[]T`, `[N]T`, `map[K]V`, `chan T`, `*T`, and `struct{}` (value `struct{}{}`).
- Function types are `(A, B) -> R`, `(A) -> unit`, and `(A) -> R throws` for one that can fail.
- `T?` is nullable. `[]User?` is a slice of nullable users: `?` belongs to the element. Slices
  and maps themselves are never nullable; an empty one is the absent value.
- Numbers never convert implicitly: `float64(n)`, `int64(x)`.
- Literals take their type from context: `xs: []int = []`, `m: map[string]int = [:]`,
  `["a": 1]`. Go's typed literals work too: `[]int64{1, 2}`, `map[string]any{"ok": true}`.
- `m[k]` and `ch.receive()` give the zero value when nothing is there, as in Go; when the value
  type has none (a struct with a non-null pointer field, a `*User`), they give a `T?` that is
  null then: `job := jobs.receive() ?: break`.
- Slicing is Go's: `xs[lo:hi]`, `xs[lo:hi:max]`. Conversions are `T(x)`, and `(*T)(p)` for a
  pointer type (`unsafe.Pointer` works as in Go).

## Errors

- A function that can fail is `func f(): T throws`, Go's `(T, error)`.
- `f()?` returns the error to the caller. `throw err` returns one. `err.wrap("loading ${path}")`
  wraps it with context.
- `f() catch { fallback }`, or `f() catch (err) { ...; fallback }`, handles it in place.
  `f() catch {}` ignores it explicitly.
- `f() catch (e: *NotFound) { ... }` handles one error type (through wrapping); it must end with
  `?` or a final `catch` for the other errors. For an error already in a variable, use
  `nf, ok := errors.AsType[*NotFound](err)`: `err is *NotFound` sees only the outermost error and
  misses a wrapped one (warning W0007).
- An error that is not handled is a compile error. `func main() throws` prints the error and
  exits with status 1, and tests may be `throws`.
- In a `throws` function, `defer f.Close()?` makes a failure of the deferred call the function's
  error, unless the function is failing already. `?` and `throw` inside `defer { ... }` do the
  same: `defer { r := recover(); if r != null { throw fmt.Errorf("panic: %v", r) } }`.
- `n, err := strconv.Atoi(s)` also works, as in Go.

## Null safety

- A `T?` needs a check before member access: `if u != null { u.Name }` (a smart cast), or
  `u?.Name`, `u?.Name ?: "anon"`, `u!!` (panics on null).
- `x := f() ?: return` exits early on null.
- Smart casts apply to parameters, `self`, and locals. A reassigned local stays smart-cast until
  its next assignment (`node = node.Next` inside `while node != null` works, and so does
  `if u == null { u = guest }` before `u.Name`), unless a lambda assigns it or `&u` is taken.
  For a field or a package-level variable, copy it into a local first.
- `v, err := f()` gives `err` the type `error?`: check it before `throw err`. A success value
  without a zero value is `T?` there (null when `f` fails); prefer `v := f()?`.
- Values from Go packages are usable as non-null.

## Control flow

- No parentheses around conditions, braces always: `if c { } else if d { } else { }`,
  `while c { }`, `while true { }`.
- Loops: `for x in xs`, `for i, x in xs`, `for k, v in m`, `for i in 0..<n`, `for i in 1..n`,
  and Go iterators: `for k in maps.Keys(m)`, `for k, v in maps.All(m)`. There is no `range`.
  Go's three-part loop works for other steps: `for i := n - 1; i >= 0; i-- { }`.
- `if` takes Go's init statement: `if v, ok := m[k]; ok { }`, `if r := recover(); r != null { }`.
- `if` and `when` are expressions: `label := if n > 0 { "pos" } else { "neg" }`.
  `when code { 200, 204 -> a; in 500..599 -> b; else -> c }`, and `is T ->` branches over
  interfaces. A `when` over an enum or a
  sealed interface must cover every case or have `else`; over a nullable one, every case and
  `null` (`null -> ...`) make it exhaustive too.
- `x in xs`, `k in m`, `"@" in email`, `code in 200..299`, and `!in`.
- Labels: `outer@ for x in xs { ... break@outer }`. `break` inside `when` exits the loop.
- Lambdas: `{ a, b -> a + b }`, `{ it * 2 }`, and a trailing lambda `xs.Map { it * 2 }`. A loop
  or `if` header cannot end in a trailing lambda: parenthesize the call. A lambda with several
  results returns them with `return a, b`.
- `go { ... }`, `defer { ... }`, `defer f.Close() catch {}`.
- Channels: `make(chan int, 10)`, `ch.send(v)`, `ch.receive()`, `v, ok := ch.receive()`,
  `ch.close()`, `for v in ch`, and `select { v := ch.receive() -> ...; else -> ... }`.

## Tests

- Tests are Go tests in `_test.lyth` files: `func TestParse(t: *testing.T) { ... }`, and a test
  may be `throws`. A test file in `package x_test` tests package `x` from outside, importing it
  as other code does. `lyth test ./...` runs them, with `go test` flags such as `-v` and `-run`.
- A table test declares its case struct in the test function:
  `struct tc { in: string; want: int }`, then `for c in [tc(in = "1", want = 1)] { ... }`.
- `go vet` runs with the tests and requires a constant format string: write
  `t.Errorf("got %d, want %d", got, want)`, or `t.Error("got ${got}, want ${want}")` with a
  template, never a template inside `t.Errorf` (`lyth check` warns, W0010).

## Coming from Go

What Go programmers write by habit, and the Lyth for it:

| Go | Lyth |
|---|---|
| `func (u *User) Rename(n string)` | `func *User.Rename(n: string)`, with `self` for the receiver |
| `x int`, `func f() int` | `x: int`, `func f(): int` |
| `Point{X: 1, Y: 2}` | `Point(X = 1, Y = 2)` |
| `gin.H{"error": msg}`, `url.Values{"q": {"go"}}` | `gin.H(["error": msg])`, `url.Values(["q": ["go"]])` |
| `for i, x := range xs` | `for i, x in xs` |
| `for i := 0; i < n; i++` | the same, or `for i in 0..<n` |
| `for { }`, `for cond { }` | `while true { }`, `while cond { }` |
| `ch <- v`, `<-ch`, `close(ch)` | `ch.send(v)`, `ch.receive()`, `ch.close()` |
| `go func() { ... }()` | `go { ... }` |
| `nil` | `null` |
| `if err != nil { return err }` | `f()?` |
| `defer func() { if cerr := f.Close(); cerr != nil && err == nil { err = cerr } }()` | `defer f.Close()?` |
| `[T cmp.Ordered]` | `[T: cmp.Ordered]` |
| `var x = 1` in a function | `x := 1` |
| `var mu sync.Mutex` (package level) | `var mu: sync.Mutex` |
| `type tc struct { ... }` in a function | `struct tc { ... }` in the function |
| `[]struct{ in string }{...}` | declare `struct tc { in: string }`, then `[tc(in = "a")]` |
| `switch` | `when` |
| `if v := f(); v > 0 { }` | `v := f()`, then `if v > 0 { }` |
| `ctx, cancel := context.WithTimeout(ctx, d)` | the same: `:=` may replace a parameter with a value derived from it |
| `r = r.WithContext(ctx)` (assigning a parameter) | `r := r.WithContext(ctx)` |
| `strings.FieldsFunc(s, func(r rune) bool { ... })` | `strings.FieldsFunc(s) { r -> ... }` |
| `func(string) bool`, `func(string) error` (types) | `(string) -> bool`, `(string) -> unit throws` |
| `x.(T)`, `v, ok := x.(T)`, `_, ok := x.(T)` | `x as T`, `v := x as? T`, `ok := x is T` |
| `switch x := v.(type) { case int: ... }` | `when x := v { is int -> ... }` |
| `const ( A T = iota; B )`, `var ( x int )` | `const ( A: T = iota; B )`, `var ( x: int )` |
| `func f() (n int, err error)` (named results) | `func f(): (n: int) throws` |
| a label on its own line above a loop | `outer@ for ...` |

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
| methods inside the class | `func User.Greet()` at top level |
| `companion object`, static methods | package functions (`NewUser`) |
| `listOf(1, 2)`, `mapOf("a" to 1)` | `[1, 2]`, `["a": 1]` |
| `when (val x = f())` | `when x := f()` |
| `for (x in xs)` | `for x in xs` |
| `try { } catch (e: E) { }` | `f() catch (e: *E) { } catch { }`, or end with `?` |
| `xs.filter { }.map { }` | `import "lyth/coll"`, then `xs.Filter { }.Map { }` |

## Not in Lyth

Packages mixing Lyth and non-test Go files (put the Go code in a package of its own),
anonymous struct types (declare a struct, also inside a function), and `T?` on type parameters
(return `(T, bool)`) are left out by design. The compiler reports each of these with a way
around it.

## Changes between versions

Lyth's syntax has changed and will change again. The compiler keeps recognizing older forms and
reports each with the edit to today's form; `lyth fix` applies those edits to a whole module.
The table lists older Lyth forms and the Go and Kotlin habits the compiler recognizes.

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
| `"$name"`, where `name` is a variable | `"${name}"`, or `"\$name"` for the text | no: both are possible |
| `for (x in xs)` | `for x in xs` | yes |
| `when (val x = e)` | `when x := e` | yes |
| `(a, b) = f()` | `a, b = f()` | yes |
| `Point{X: 1}` | `Point(X = 1)` | yes |
| `gin.H{"k": v}` | `gin.H(["k": v])` | yes |
| `strings.split(s, ",")` | `strings.Split(s, ",")` | yes |
| `go func() { ... }()` | `go { ... }` | yes |
| `ch <- v`, `<-ch` | `ch.send(v)`, `ch.receive()` | yes |
| `const userKey ctxKey = 1` | `const userKey: ctxKey = 1` | yes |
| `catch (e: error) { ... }` | `catch (e) { ... }`: it handles every error | yes |
| `catch (e: PathErr)` where only `*PathErr` has the `Error` method | `catch (e: *PathErr)` | yes |
| `val`, `var` as names (`val := m[k]`) | another name, such as `value` | no: a name to choose |
| `try { ... } catch (e: Exception) { ... }` | `f() catch (e) { ... }` on each call that can fail | no |
| `outer: for ...`, `break outer` (also with the label on its own line) | `outer@ for ...`, `break@outer` | yes |
| `func(string) bool` (a Go function type) | `(string) -> bool` | yes; `func(...) error` is a hint: `(...) -> unit throws`, or `-> error?` |
| `import "net/http" as h` | `import h "net/http"` | yes |
| `typealias Handler = (Ctx) -> unit` | `type Handler = (Ctx) -> unit` | yes |
| `type User struct { ... }`, `type Box[T any] struct` | `struct User { ... }`, `struct Box[T: any]` | yes (the name, keyword, and type parameters) |
| `Name Type` (a Go struct field), `X, Y int` | `Name: Type`, one field per line | yes |
| `func f(name string, a, b int) int` | `func f(name: string, a: int, b: int): int` | yes |
| `Area() float64` in an interface | `Area(): float64` | yes |
| `func f() (int, error)`, `func f() error` | `func f(): int throws`, `func f() throws` | no: the returns change |
| `func (p *T) M()` | `func *T.M()`, and `self` in the body | no |
| a field or method by bare name in a method (`Count++`, `validate()`) | `self.Count++`, `self.validate()` | yes, unless the name also names a package-level declaration or import there |
| automatic json key `iDs` for `IDs`, `urLs` for `URLs`, `iPv4` for `IPv4` | the key is now `ids`, `urls`, `ipv4`; tag `json:"iDs"` keeps the old one (W0009) | no: a wire change |
| a null `T?` field encoded as `null` | it is left out (`omitzero`); tag `json:"name"` keeps `null` | no: a wire change |
| a `T?` passed to a Go `any` parameter arrived as a pointer (`*string`) | it arrives as its value, or a nil interface when null; pass `&x` where Go writes through a pointer | no: a run-time change |
| an enum encoded as its number in JSON | its name (`"pendingReview"`); declare `MarshalJSON` and `UnmarshalJSON` to keep numbers | no: a wire change |
| `n, err := strconv.Atoi(s)` then `throw err` or passing `err` as `error` without a check | `if err != null { throw err }`: the destructured error is `error?` (E0305, E0401 otherwise) | no: a check to add |
| `lyth run main.lyth` inside a module ran the file's whole package | it runs that file alone, as `go run main.go` does; `lyth run .` runs the package | no: a command to change |
| `json:""` on a field kept an empty tag | the Go field is untagged, so encoding/json uses the field name | no: a wire change only where the tag was written |
| a field or method `len`, `init`, ... was `len_`, `init_` in Go | it keeps its name; Go code of the package uses `x.len` | no: Go code to change |

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
