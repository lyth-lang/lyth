# Lyth for Go programmers

If you know Go, you already know Lyth's runtime, standard library, packages,
concurrency model, and performance. Lyth changes how everyday code reads, and
moves two classes of runtime failure (nil dereferences and ignored errors) to
compile time. This page compares the two side by side, including the places
where Lyth is no simpler than Go.

You also know most of Lyth's spelling. For everything Go already has, Lyth
writes it Go's way: `func`, `:=` and `=`, Go's type names (`int`, `string`,
`error`, `any`, `map[K]V`, `chan T`), generics in square brackets
(`Max[T: cmp.Ordered]`, `errors.AsType[*E](err)`), conversions such as
`float64(n)`, and `if` and `for` headers without parentheses. Lyth
syntax appears where Lyth adds something Go lacks or changes a meaning Go would
get wrong; the [complete list](#what-lyth-spells-differently) is at the end of
this page, with the reason for each difference.

Each example's **Go** tab is what `lyth gen` produces from it. It is the code
the Go compiler builds, so the comparison is exact: there is no hidden runtime
or wrapper library. The generated code follows Go conventions closely, with a
few telltale signs such as generated temporary names (`v`, `v2`).

## Error propagation

```kotlin
import "errors"
import "strconv"
import "strings"

struct Order {
    Item: string
    Qty: int
}

func parseOrder(line: string): Order throws {
    item, qty, found := strings.Cut(line, "=")
    if !found {
        throw errors.New("missing = in ${line}")
    }
    return Order(Item = item, Qty = strconv.Atoi(qty)?)
}

func parseAll(lines: []string): []Order throws {
    orders: []Order = []
    for line in lines {
        orders = append(orders, parseOrder(line)?)
    }
    return orders
}

func main() {
    orders := parseAll(["tea=2", "cake=1"]) catch (err) {
        println("error:", err)
        return
    }
    println(len(orders), orders[0].Item, orders[1].Qty)
    parseAll(["tea=2", "cake"]) catch (err) { println("error:", err) }
}
```

Every `?` is an `if err != nil { return ..., err }` in the Go tab. Lyth writes
the zero values (`Order{}`, `nil`) for you, which is where hand-written Go
error handling most often goes wrong. A call whose error is neither propagated
nor handled is a compile error, not a `go vet` warning.

## Building a value from fallible parts

```kotlin
import "strconv"
import "time"

struct Config {
    Port: int
    Timeout: time.Duration
    Debug: bool
}

func load(env: map[string]string): Config throws {
    return Config(
        Port = strconv.Atoi(env["PORT"])?,
        Timeout = time.ParseDuration(env["TIMEOUT"])?,
        Debug = strconv.ParseBool(env["DEBUG"])?,
    )
}

func main() {
    cfg := load(["PORT": "8080", "TIMEOUT": "5s", "DEBUG": "true"]) catch (err) {
        println(err)
        return
    }
    println(cfg)
    load(["PORT": "80", "TIMEOUT": "soon", "DEBUG": "no"]) catch (err) { println("error:", err) }
}
```

This is where Lyth saves the most. Go needs a temporary and an error check per
field before it can build the value; Lyth keeps one expression, and evaluation
still goes left to right, so the first failing field is the one reported.

## Errors and absence together

```kotlin
import "errors"

struct User {
    Name: string
    Email: string?
}

var users: map[string]User = [
    "ada": User(Name = "Ada", Email = "ada@example.com"),
    "bob": User(Name = "Bob", Email = null),
]

func find(id: string): User? throws {
    if id == "" {
        throw errors.New("empty id")
    }
    user, ok := users[id]
    if !ok {
        return null
    }
    return user
}

func describe(id: string): string throws {
    user := find(id)? ?: return "nobody"
    email := user.Email ?: "no email"
    return "${user.Name} <${email}>"
}

func main() {
    for id in ["ada", "bob", "eve", ""] {
        println(describe(id) catch (err) { "error: ${err}" })
    }
}
```

`User?` is a `*User` in Go, so "not found" is a value and failure is an error,
as idiomatic Go APIs do it. The difference is that Lyth checks it: `user.Name`
does not compile until `user` is known to be non-null, while Go compiles a
missing nil check and panics at run time.

## Null-safe chains

```kotlin
struct Address {
    City: string
}

struct Customer {
    Address: Address?
}

struct Order {
    Customer: Customer?
}

func cityOf(o: Order?): string = o?.Customer?.Address?.City ?: "unknown"

func main() {
    full := Order(Customer = Customer(Address = Address(City = "Oslo")))
    println(cityOf(full), cityOf(Order(Customer = Customer(Address = null))), cityOf(null))
}
```

One line in Lyth; a ladder of nil checks in Go.

## No typed-nil errors

```kotlin
struct ValidationError {
    Field: string
}

func *ValidationError.Error(): string = "invalid ${self.Field}"

func validate(name: string): *ValidationError? = if name == "" { &ValidationError(Field = "name") } else { null }

func check(name: string): error? = validate(name)

func main() {
    println(check("ada") == null, check(""))
}
```

Written the same way in Go, `check("ada")` returns a non-nil `error`: the
interface holds a nil `*ValidationError`, so `err != nil` is true and the
caller reports a failure that never happened. It is one of Go's best-known
mistakes. Lyth knows the pointer is null and converts it to a nil interface,
which costs one nil check (`if v == nil { return nil }` in the Go tab). The
same rule makes an `int?` converted to `any?` hold an `int` or nothing.

## Fallbacks and parsing

```kotlin
func portOr(s: string, fallback: int): int = s.toInt() catch { fallback }

func percent(s: string): int throws {
    n := s.toInt() catch { throw Problem(Field = "percent", Reason = "not a number: ${s}") }
    if n < 0 || n > 100 {
        throw Problem(Field = "percent", Reason = "out of range: ${n}")
    }
    return n
}

struct Problem {
    Field: string
    Reason: string
}

func Problem.Error(): string = "${self.Field}: ${self.Reason}"

func main() {
    println(portOr("8080", 80), portOr("http", 80))
    println(percent("42") catch { -1 })
    percent("142") catch (err) { println(err) }
    percent("lots") catch (err) { println(err) }
}
```

`catch { value }` replaces a failed result with a fallback, and
`catch { throw ... }` turns a low-level error into a domain error. `Problem`
implements Go's `error` interface by having an `Error()` method, like any
Go type. `s.toInt()` is one of a handful of prelude methods; it lowers to
`strconv.Atoi`.

## Wrapping errors with context

```kotlin
import "errors"
import "os"

func loadProfile(user: string, attempt: int): string throws {
    data := os.ReadFile("/profiles/${user}.json") catch (err) {
        throw err.wrap("loading profile of ${user} (attempt ${attempt})")
    }
    return string(data)
}

func main() {
    loadProfile("ada", 2) catch (err) {
        println(err)
        println("not found:", errors.Is(err, os.ErrNotExist))
    }
}
```

`err.wrap` with a template becomes a single `fmt.Errorf` with format verbs and
`%w`, the same call (and the same allocations) as hand-written Go.

## Closing resources

```kotlin
import "errors"

struct Conn {
    name: string
}

func *Conn.Close() throws {
    println("closing", self.name)
    if self.name == "flaky" {
        throw errors.New("close ${self.name}: connection reset")
    }
}

func dial(name: string): *Conn throws = &Conn(name = name)

func query(name: string): string throws {
    c := dial(name)?
    defer c.Close()?
    return "rows from ${name}"
}

func main() {
    println(query("db") catch (err) { "error: ${err}" })
    println(query("flaky") catch (err) { "error: ${err}" })
}
```

Go code that reports the error of a deferred `Close` needs a named result
and a deferred closure that sets it when nothing else failed; the Go tab
shows it. `defer c.Close()?` is that closure. A `defer { ... }` block may use
`?` and `throw` the same way, which turns a recovered panic into an error.

## Handling one kind of error

```kotlin
import "errors"
import "fmt"

struct NotFound {
    ID: int
}

func *NotFound.Error(): string = "user ${self.ID} not found"

func find(id: int): string throws {
    if id < 0 {
        throw errors.New("database unavailable")
    }
    if id != 1 {
        throw fmt.Errorf("find: %w", &NotFound(ID = id))
    }
    return "Ada"
}

func status(id: int): int {
    find(id) catch (_: *NotFound) {
        return 404
    } catch {
        return 500
    }
    return 200
}

func nameOr(id: int, fallback: string): string throws {
    name := find(id) catch (_: *NotFound) { fallback }?
    return name
}

func main() {
    println(status(1), status(2), status(-1))
    println(nameOr(2, "guest") catch { "?" }, nameOr(-1, "guest") catch (err) { "error: ${err}" })
}
```

`catch (e: T)` handles errors of type `T` wherever they sit in a wrapped
chain: it is `errors.AsType`, the Go 1.26 form of `errors.As`. Clauses are
tried in order. Errors that no typed clause handles must still go somewhere,
so the expression ends with `?` to propagate them or with a plain `catch` to
handle them. Go compiles the version that forgets the `return err` after an
`errors.As` check; Lyth reports it as an unhandled error (E0303).

## Optional parameters

```kotlin
import "time"

func connect(host: string, port: int = 5432, tls: bool = true, timeout: time.Duration = 5 * time.Second): string =
    "${host}:${port} tls=${tls} timeout=${timeout}"

func main() {
    println(connect("db"))
    println(connect("db", tls = false))
    println(connect("replica", 6432, timeout = time.Second))
}
```

Go code reaches for a config struct or functional options as soon as a
function has optional settings. In Lyth, parameters have defaults and calls
name the arguments they pass. The Go function keeps a plain signature, and each
call site passes every argument, so nothing is allocated or looked up at run
time. Named arguments work for functions declared in Lyth. Go functions take
positional arguments, because Go does not treat parameter names as part of an
API.

## Optional interfaces and type switches

```kotlin
import "io"
import "strings"

func writeText(w: io.Writer, s: string): int throws {
    if w is io.StringWriter {
        return w.WriteString(s)    // w is an io.StringWriter here
    }
    return w.Write([]byte(s))
}

func kind(v: any): string = when v {
    is int -> "int"
    is string -> "string of ${len(v)} bytes"
    is error -> "error: ${v.Error()}"
    else -> "something else"
}

func main() {
    sb := strings.Builder()
    println(writeText(&sb, "hello") catch { 0 }, sb.String())
    println(kind(42), kind("lyth"), kind(io.EOF), kind(1.5))
}
```

`is` smart-casts, so the branch uses `w` as the tested type without a second
name; the generated Go binds one (`if sw, ok := w.(io.StringWriter); ok`). A
`throws` function that returns another fallible call passes its results on as
they are, error included, as Go's `return w.Write(b)` does. A `when` of `is`
tests is a Go type switch. In Go, the two forms are about as long
as Lyth's; the gain is consistency with null checks rather than brevity.

## Enums and exhaustive switches

```kotlin
enum Method { Get, Post, Delete }

sealed interface Result {
    Status(): int
}

struct Ok : Result {
    Body: string
}

func Ok.Status(): int = 200

struct NotFound : Result {
    Path: string
}

func NotFound.Status(): int = 404

func allowed(m: Method): bool = when m {
    Get -> true
    Post, Delete -> false
}

func render(r: Result): string = when r {
    is Ok -> r.Body
    is NotFound -> "no page at ${r.Path}"
}

func main() {
    println(Method.Get, allowed(Method.Get), allowed(Method.Delete))
    println(render(Ok(Body = "hi")), render(NotFound(Path = "/x")))
}
```

The Go tab shows the idiom Go programmers write by hand: `type Method int`
with `iota` constants (`MethodGet`, ...) and a `String` method, and an
interface with an unexported method that only the listed structs have. What
Go cannot do is check a `switch` against the set. In Lyth, a `when` over an
enum or a sealed interface must cover every entry or member type (or have an
`else`), so adding `Method.Put` or a new `Result` makes every `when` that
forgets it a compile error. The generated `switch` ends in a `default` that
panics with the Lyth position, for values from outside the set such as
`Method(7)`.

## Channels and select

```kotlin
import "time"

func main() {
    results := make(chan int, 3)
    for i in 1..3 {
        go { results.send(i * 10) }
    }
    sum := 0
    for _ in 1..3 {
        sum += results.receive()
    }
    println(sum)

    never := make(chan int)
    select {
        v := never.receive() -> println(v)
        time.After(10 * time.Millisecond).receive() -> println("timed out")
    }
}
```

Channels are Go channels, with the operator spelled as methods: `ch.send(v)`
is `ch <- v`, `ch.receive()` is `<-ch`, `ch.close()` is `close(ch)`, and
`<-ctx.Done()` becomes `ctx.Done().receive()`. `select` is Go's `select`
with `when`-style branches; `else` is `default`, and `break` exits the
enclosing loop rather than the `select`.

## Collection operations

```kotlin
import "lyth/coll"

struct Order {
    Customer: string
    Total: int
    Paid: bool
}

func main() {
    orders := [
        Order(Customer = "ada", Total = 30, Paid = true),
        Order(Customer = "linus", Total = 12, Paid = false),
        Order(Customer = "ada", Total = 8, Paid = false),
    ]
    unpaid := orders.Filter { !it.Paid }.Map { it.Customer }.Distinct()
    byCustomer := orders.GroupBy { it.Customer }
    revenue := orders.Fold(0) { sum, o -> sum + o.Total }
    println(unpaid, byCustomer.SortedKeys(), len(byCustomer["ada"]), revenue)
}
```

Go has no generic methods, so the same pipeline in Go is either three loops or
nested helper calls. `lyth/coll` is a Lyth package of generic extension
functions: `orders.Filter { ... }` is the Go call `coll.Filter(orders, ...)`,
and the Go tab shows the chain. Each step allocates a new slice, which the
code makes visible; where that matters, a loop is still available and reads
the same as Go. The [Collection API](../design/collections.md) documents each
operation's result, empty-input behavior, and measured cost.

## Read-only parameters and shadowing

```kotlin
import "strconv"

func total(prices: []int, discount: int): int {
    discount = 20           // error[E0501]: cannot assign to parameter discount: parameters are read-only
    sum := 0
    for p in prices {
        p = p * 2           // error[E0501]: cannot assign to loop variable p
        sum += p
    }
    return sum - discount
}

func parse(a: string, b: string, verbose: bool): int {
    n, err := strconv.Atoi(a)
    if verbose {
        m, err := strconv.Atoi(b)   // warning[W0006]: err hides the variable declared in an enclosing scope
        println(m, err)
    }
    if err != null {
        return 0
    }
    return n
}
```

Locals are mutable, as in Go: `x := e` declares one and `=` assigns it.
Parameters, loop variables, `catch` bindings, and `self` are read-only, and a
value receiver's fields cannot be assigned, because the assignment would change
only the method's copy. Go has no such check, and it costs nothing at run
time: the generated code uses ordinary Go variables. A local the function never
assigns again keeps the type that a null check or `is` test gave it (a smart
cast), like a parameter.

A declaration in an inner block may hide an outer variable, as in Go, but draws
warning W0006. `parse` above has Go's shadowed-`err` bug: the inner `:=`
declares a new `err`, so the check after the block never sees the second
failure. Go compiles it silently; Lyth points at both declarations.

## Where Lyth looks like Go

```kotlin
import "encoding/json"
import "net/http"
import "net/http/httptest"

struct Greeting {
    Message: string
}

func hello(w: http.ResponseWriter, r: *http.Request) {
    name := r.URL.Query().Get("name")
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(Greeting(Message = "hello ${name}")) catch {}
}

func main() {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /hello", hello)
    rec := httptest.NewRecorder()
    mux.ServeHTTP(rec, httptest.NewRequest("GET", "/hello?name=gopher", null))
    print(rec.Code, " ", rec.Body.String())
}
```

Code that mostly calls Go APIs reads almost the same in both languages: Lyth
uses Go's names verbatim (`http.NewServeMux`, `r.URL`, `*http.Request`), and
there is no Lyth web framework in between. The differences are small: `json`
tags come from field names, and an error you choose to ignore is written
`catch {}` instead of being silently dropped.

## What Lyth spells differently

Each difference from Go's spelling names something Go lacks or changes a
meaning Go would get wrong ([ADR 0031](../adr/0031-go-spelling.md)):

| Lyth | Go | Why |
|---|---|---|
| `name: Type`, `func f(): R` | `name Type`, `func f() R` | one rule for every name and type, which reads well with `User?` and `throws` |
| `[T: cmp.Ordered]` | `[T cmp.Ordered]` | the same rule; Go's form is an error with the fix |
| `throws`, `?`, `catch`, `throw` | `(T, error)`, `if err != nil` | the main improvement; lowers to Go's convention |
| `T?`, `?.`, `?:`, `!!`, smart casts | `*T`, nil checks | nil safety at compile time |
| `null` | `nil` | Lyth's null is never a typed nil |
| `func T.M()`, `func *T.M()`, `self` | `func (t T) M()` | the receiver always has the same name and cannot be shadowed; there are no static methods, so constructors stay package functions (`NewServer`) |
| `for x in xs`, `for i, x in xs`, `for i in 0..<n` | `for _, x := range xs`, `for i := 0; i < n; i++` | element first, and ranges `a..<b`, `a..b`; Go's `range` would be a false friend, and `for i, x := range xs` is an error whose hint gives the Lyth form |
| `while c`, `while true` | `for c`, `for` | `for` only iterates |
| `when x { ... }` | `switch x { ... }` | an expression, exhaustive over enums and sealed interfaces; `break` exits the loop |
| `if`/`when` as values; `= expr` bodies | statements | fewer temporaries |
| `ch.send(v)`, `ch.receive()`, `ch.close()` | `ch <- v`, `<-ch`, `close(ch)` | `<-` appears only in the types `chan<- T` and `<-chan T` |
| read-only parameters and loop variables; a value receiver's fields cannot be assigned | assignable | an assignment to a parameter or a copy is almost always a mistake |
| shadowing warns (W0006) | allowed silently | catches the shadowed `err` |
| named and default arguments | positional only | optional settings without an options struct |
| `x as T`, `v := x as? T`, `x is T` | `x.(T)`, `v, ok := x.(T)` | the same words as other type tests; `is` smart-casts, so the branch needs no second name |
| `(string) -> bool`, `(string) -> unit throws` | `func(string) bool`, `func(string) error` | a function type reads like the lambdas that fill it |
| `outer@ for ...`, `break@outer` | `outer:` above the loop | the label is part of the loop it names |
| one `var` or `const` per line; `enum` | `var ( ... )`, `const ( ... iota )` | an enum gives names on the wire and exhaustive `when`; Lyth has no `iota` |
| unnamed results; `defer f.Close()?` | named results | the one use Go needs them for, a deferred error, is `?` in `defer` |

A few of these make Lyth slightly *longer* than Go: `name: Type` costs a
character per parameter, `send(...) catch {}` is longer than an ignored
`send(...)` because discarded errors are explicit, and `ch.receive()` is longer
than `<-ch`.

## What Lyth does not change

- The spelling of what Go already has: `func`, `:=`, `const`, `go`, `defer`,
  `select`, the predeclared types, `[]T`, `map[K]V`, `chan T`, generics in
  square brackets, conversions such as `[]byte(s)`, and headers without
  parentheses.
- Goroutines, `sync`, `context`, and the memory model.
- Names and visibility: a capitalized name is exported, and a Lyth name is its
  Go name, so stack traces, `go doc`, and Go callers show what you wrote.
- Interfaces are structural; method sets and pointer receivers follow Go.
- Slices, maps, zero values, `defer`, `panic`, and `recover`.
- Packages, modules, `go.mod`, build tags, cross-compilation, and the race
  detector: `lyth` drives the `go` command.
- Performance: across 18 benchmarks, Lyth and hand-written Go show no
  measurable difference in time, allocations, or memory
  ([benchmarks](../design/benchmarks.md)).

## In numbers

Across the benchmark suite's seven workloads, written by hand in both
languages, the Lyth versions are 19% shorter in total (277 versus 344 lines).
The spread is wide: 37% shorter for error-heavy code, 11% for allocation-heavy
loops, and 3% for an HTTP handler. Lyth pays off most where Go code is mostly
error and nil plumbing.
