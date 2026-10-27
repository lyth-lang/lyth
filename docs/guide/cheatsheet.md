# Cheat sheet

Lyth syntax next to the Go it compiles to. The [tour](tour.md) explains each
row with runnable examples, and the [language reference](../spec/language.md)
has the exact rules.

## Declarations

| Lyth | Go |
|---|---|
| `x := 1`, `x = 2`, `lo, hi := 0, n`, `a, b = b, a + b` | the same |
| `x: int64 = 0`, `x: int64` (zero value) | `var x int64 = 0`, `var x int64` |
| `var Default = "x"`, `const Max = 10` (package level) | the same |
| `func add(a: int, b: int): int = a + b` | `func add(a int, b int) int { return a + b }` |
| `func square(n: int) = n * n` (unexported) | `func square(n int) int { return n * n }` |
| `func dial(host: string, port: int = 5432)` | `func dial(host string, port int)`; calls fill in `5432` |
| `func Add(...)` (capitalized: exported) | the same |
| `func parse(s: string): int throws` | `func parse(s string) (int, error)` |
| `struct User { Name: string }` | ``type User struct { Name string `json:"name"` }`` |
| `struct Circle : Shape { R: float64 }` | `type Circle struct { ... }` and `var _ Shape = Circle{}` |
| `secret: string` (lower-case field) | `secret string` (unexported, no `json` tag) |
| `struct Item { Base; Name: string }` (a type alone on a line embeds it) | ``type Item struct { Base; Name string `json:"name"` }`` |
| `func User.Hello(): string = "hi ${self.Name}"` (members through `self`) | `func (u User) Hello() string { return "hi " + u.Name }` |
| `func *User.rename(n: string)` (pointer receiver) | `func (u *User) rename(n string)` |
| `interface Shape { Area(): float64 }` | `type Shape interface { Area() float64 }` |
| `type Handler = (Ctx) -> unit throws` | `type Handler = func(Ctx) error` |
| `type UserID int64`, `type Role string`, `func Role.Valid(): bool` | `type UserID int64`, `type Role string`, `func (r Role) Valid() bool` |
| `func maxOf[T: cmp.Ordered](a: T, b: T): T` | `func maxOf[T cmp.Ordered](a T, b T) T` |
| `struct Page[T] { Items: []T }`, `func Page[T].First(): T` | `type Page[T any] struct { ... }`, `func (p Page[T]) First() T` |
| `func string.Shout(): string` (extension), `"hi".Shout()` | `func Shout(s string) string`, `Shout("hi")` |
| `enum Color { Red, Green }` | `type Color int`, `const ( ColorRed Color = iota; ColorGreen )`, `String`, and `MarshalText`/`UnmarshalText` with the names `"red"`, `"green"` |
| `enum Plan: string { Free = "free" }` | `type Plan string`, `const PlanFree Plan = "free"`, and an `UnmarshalText` that accepts only the values |
| `sealed interface Shape { ... }` and `struct Circle : Shape` | an interface with an unexported `isShape()` that only declared members have |
| `import h "net/http"` | `import h "net/http"` (the same) |
| `import (` / `"fmt"` / `h "net/http"` / `)` | `import (` / `"fmt"` / `h "net/http"` / `)` |

## Types

| Lyth | Go |
|---|---|
| `int`, `int64`, `float64`, `bool`, `string`, `byte`, `rune`, `any`, `error` | the same |
| `[]T`, `[]User?` (slice of nullable users) | `[]T`, `[]*User` |
| `map[K]V`, `[4]int`, `*T`, `chan T`, `chan<- T`, `<-chan T` | the same |
| `(A, B) -> R`, `(A) -> unit` | `func(A, B) R`, `func(A)` |
| `(A) -> R throws` | `func(A) (R, error)` |
| `User?` (optional value: never changes, `==` by contents; `*p` copies a pointer into one) | `*User` |
| `*User?` (nullable pointer: changes through it are shared), `Shape?` | `*User`, `Shape` (nil allowed) |
| `T!` (in diagnostics) | a Go value of unknown nullability |

## Expressions

| Lyth | Go |
|---|---|
| `"Hi ${name}, ${n + 1}"` | `"Hi " + name + ", " + strconv.Itoa(n+1)` |
| `User(Name = "a", Age = 3)` | `User{Name: "a", Age: 3}` |
| `dial("db", timeout = time.Second)` | `dial("db", 5432, time.Second)` (defaults filled in) |
| `code in 200..299`, `code !in 200..<300` | `code >= 200 && code <= 299`, `code < 200 \|\| code >= 300` |
| `x in xs`, `k in m`, `"@" in email` | `slices.Contains(xs, x)`, `_, ok := m[k]`, `strings.Contains(email, "@")` |
| `&User(Name = "a")` | `&User{Name: "a"}` |
| `[1, 2, 3]`, `["a": 1]`, `[:]` | `[]int{1, 2, 3}`, `map[string]int{"a": 1}`, `map[K]V{}` |
| `[]int64{1, 2}`, `map[string]any{"ok": true}`, `[3]int{1, 2}` (typed literals) | the same |
| `gin.H(["ok": true])`, `h: http.Header = ["Accept": ["text/plain"]]` | `gin.H{"ok": true}`, `h := http.Header{"Accept": {"text/plain"}}` |
| `[][]int{[1, 2]}`, `[]Point{Point(1, 2)}` | `[][]int{{1, 2}}`, `[]Point{{X: 1, Y: 2}}` |
| `if c { a } else { b }` (as a value) | an `if` statement assigning (or returning) the value |
| `when x { 1, 2 -> a; else -> b }` | `switch x { case 1, 2: ...; default: ... }` |
| `{ x -> x * 2 }`, `{ it + 1 }` | `func(x int) int { return x * 2 }` |
| `f(a) { x -> ... }` (trailing lambda) | `f(a, func(x T) { ... })` |
| `int64(x)`, `string(bytes)`, `[]byte(s)`, `make(map[string]int)` | the same |
| `errors.AsType[*E](err)`, `maxOf[int](a, b)` (when inference cannot bind) | the same |

## Errors

| Lyth | Go |
|---|---|
| `n := parse(s)?` | `n, err := parse(s); if err != nil { return ..., err }` |
| `throw errors.New("bad")` | `return ..., errors.New("bad")` |
| `parse(s) catch { 0 }` | `n, err := parse(s); if err != nil { n = 0 }` |
| `parse(s) catch (err) { log(err); 0 }` | the same, with code in the `if` |
| `f() catch {}` | `_ = f()` |
| `find(id) catch (_: *NotFound) { guest }?` | `if _, ok := errors.AsType[*NotFound](err); ok { u = guest } else { return ..., err }` |
| `return w.Write(b)` (in a `throws` function) | `return w.Write(b)` |
| `err.wrap("loading ${path}")` | `fmt.Errorf("loading %s: %w", path, err)` |
| `data := load(path).wrap("loading ${path}")?` | `data, err := load(path); if err != nil { return ..., fmt.Errorf("loading %s: %w", path, err) }` |
| `n, err := strconv.Atoi(s)` | the same |
| `func main() throws` | `main` that prints the error and exits with status 1 |

## Nulls and types

| Lyth | Go |
|---|---|
| `if u != null { u.Name }` | `if u != nil { u.Name }` |
| `u?.Name` | `var name *string; if u != nil { name = new(u.Name) }` |
| `u?.Name ?: "anon"` | `name := "anon"; if u != nil { name = u.Name }` |
| `u := find(id) ?: return` | `u := find(id); if u == nil { return }` |
| `u!!` | `u`, with a nil check that panics with the `.lyth` position |
| `x is Circle` | `_, ok := x.(Circle)` |
| `if x is Circle { x.R }` | `if c, ok := x.(Circle); ok { c.R }` |
| `x as Circle`, `x as? *Circle` | `x.(Circle)`, `c, _ := x.(*Circle)` (a value type is boxed: `Circle?` is `*Circle`) |
| `when x { is A -> ...; is B -> ... }` | `switch x := x.(type) { case A: ...; case B: ... }` |
| `Color.Red`; `when c { Red -> ...; Green -> ... }` (every entry, no `else`) | `ColorRed`; a `switch` with a panicking `default` |

## Control flow

| Lyth | Go |
|---|---|
| `for x in xs`, `for i, x in xs` | `for _, x := range xs`, `for i, x := range xs` |
| `for k, v in m` | `for k, v := range m` |
| `for i in 0..<n`, `for i in 1..n` | `for i := range n`, `for i := 1; i <= n; i++` |
| `for k in maps.Keys(m)`, `for k, v in seq2` | `for k := range maps.Keys(m)`, `for k, v := range seq2` |
| `while c { ... }`, `while true { ... }` | `for c { ... }`, `for { ... }` |
| `break` inside `when` | `break` with a loop label (Lyth's `break` always exits the loop) |
| `outer@ for x in xs { ... break@outer }` | `outer: for ... { ... break outer }` |
| `when code { in 200..299 -> a; else -> b }` | `if code >= 200 && code <= 299 { ... } else { ... }` |
| `go { ... }`, `defer { ... }` | `go func() { ... }()`, `defer func() { ... }()` |
| `make(chan int, 10)` | the same |
| `ch.send(v)`, `ch.receive()`, `ch.close()` | `ch <- v`, `<-ch`, `close(ch)` |
| `v, ok := ch.receive()` | `v, ok := <-ch` |
| `job := jobs.receive() ?: break` | `job := <-jobs; if job == nil { break }` |
| `select { v := ch.receive() -> ...; else -> ... }` | `select { case v := <-ch: ...; default: ... }` |
| `defer f.Close() catch {}` | `defer func() { _ = f.Close() }()` |

## Commands

| Lyth | Go |
|---|---|
| `lyth run file.lyth`, `lyth run ./cmd/api` | `go run` |
| `lyth build`, `lyth test`, `lyth vet` | `go build`, `go test`, `go vet` (through an overlay) |
| `lyth check` | type-check only, no build |
| `lyth fix` | rewrite older Lyth syntax and Go and Kotlin spellings |
| `lyth gen -stdout` | print the generated Go |
| `lyth cover -func=c.out` | `go tool cover -func` for a profile of Lyth code |
| `lyth mod tidy` | `go mod tidy` that also sees imports in `.lyth` files |

See [tooling](tooling.md) for flags and the build layout.
