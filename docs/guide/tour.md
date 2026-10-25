# A tour of Lyth

Lyth is a thin layer over Go: the same runtime, standard library, packages,
goroutines, and performance, spelled like Go where Go has the concept, with
a few additions and a type system that catches nil dereferences and
ignored errors at compile time. This tour shows the language by example.

Examples on this site have up to three tabs. **Lyth** is the source, **Go** is
what `lyth gen` produces for it with the current compiler, and **Output** is what
the program prints. Switching to the Go tab on any example switches all of them, so
you can read a whole page in either language. The rules behind each feature are
in the [language reference](../spec/language.md).

## Hello, Lyth

```kotlin
func main() {
    name := "Lyth"
    println("Hello, ${name}!")
}
```

Files end in `.lyth`. `println` prints to standard output, and `${...}` puts any
expression into a string. The template becomes plain string concatenation, with
`strconv` for numbers, rather than a `fmt.Sprintf` call.

## Values and variables

```kotlin
func main() {
    limit := 3              // declares; the type is inferred
    count := 0
    while count < limit {
        count++
    }
    ratio: float64 = 2.5
    println(count, ratio * float64(count))
}
```

`:=` declares a variable and `=` assigns to it, as in Go. Types are inferred,
or written after the name: `ratio: float64 = 2.5`, or `total: int` for the zero
value. The types are Go's (`int`, `float64`, `string`, `bool`, ...), and numbers
never convert implicitly: `ratio * count` would be an error that suggests
`float64(count)`. Local variables can be reassigned; parameters, loop
variables, and a method's receiver are read-only
([types](../spec/types.md#7-mutability)). A declaration that hides a variable
of an enclosing scope draws a warning. At package level, variables are declared
with `var` and constants with `const`.

## Functions

```kotlin
import "strings"

func area(w: float64, h: float64) = w * h

func Greet(name: string, times: int = 1, sep: string = ", "): string {
    parts: []string = []
    for _ in 0..<times {
        parts = append(parts, "hi ${name}")
    }
    return strings.Join(parts, sep)
}

func main() {
    println(area(3, 4))
    println(Greet("Ada"), Greet("Ada", 2), Greet("Ada", times = 3, sep = " "))
}
```

Functions are declared with `func`; parameters are written `name: Type`, and
the result type follows a colon. A block body returns with `return`; a body
that is one expression can use `=`, and an unexported one can leave out its
result type, which is the expression's (`float64` here).
Visibility works as in Go: a name that starts with an uppercase letter
(`Greet`) is exported, visible to other packages, and its result type is always
written out because it is part of the package's API. A lower-case name (`area`)
stays inside its package. The name you write is the name in the generated Go,
in stack traces, and in `go doc`.

Parameters are read-only and may have default values. Calls to Lyth functions
can name their arguments, after any positional ones and in any order. The Go
function keeps a plain signature: each call passes the defaults it leaves out.
Go packages are imported by their import path and used with their exact Go
names, and take positional arguments only.

## Structs and methods

```kotlin
import "fmt"

struct Point {
    X: int
    Y: int
}

func Point.plus(o: Point): Point = Point(X = X + o.X, Y = Y + o.Y)

func *Point.moveBy(dx: int) {
    X += dx
}

// String makes Point a fmt.Stringer, so printing uses it.
func Point.String(): string = "(${X}, ${Y})"

func main() {
    p := Point(1, 2)
    p.moveBy(10)
    println(p.plus(Point(X = 1, Y = 1)))
    fmt.Printf("%v and %+v\n", p, p)
}
```

A struct is constructed by calling it, with named arguments in any order or
positional arguments for all fields. Methods are declared outside the struct,
as in Go: `func Point.plus` has a value receiver and `func *Point.moveBy` a
pointer receiver. Inside a method, the receiver is `self`, and fields and
methods are also in scope by name (`X` is `self.X`). A value receiver is a
copy, so assigning to its fields is a compile error; `func *T.M()` declares a
pointer receiver, which sees and can change the caller's value. There are no
static methods: a constructor is a package function such as `NewPoint`.
Capitalized fields are exported, as in Go, and get `json` tags in lower camel
case (`X` becomes `"x"`, `UserID` becomes `"userID"`), so structs serialize the
way JSON APIs expect. Lower-case fields stay private to the package and are not
serialized.

## Collections

```kotlin
func main() {
    primes := [2, 3, 5, 7]
    squares: []int = []
    for p in primes {
        squares = append(squares, p * p)
    }
    ages := ["ada": 36, "linus": 54]
    ages["guido"] = 67
    println(squares, len(ages), 5 in primes, "grace" !in ages)
    for i, p in primes {
        if i >= 2 {
            println("primes[${i}] =", p)
        }
    }
}
```

`[]T` is a Go slice and `map[K]V` a Go map, with Go's built-ins (`len`,
`append`, `delete`, `make`) and Go's packages (`slices`, `maps`). A slice
shares its backing array, as in Go, so `append` and element assignments can be
visible through other slices of the same array. In a slice type, `?` belongs
to the element: `[]User?` holds nullable users. `for x in xs` ranges
over elements, `for i, x in xs` over index and element, `for k, v in m` over
keys and values, and `for i in a..<b` over numbers. An empty literal needs a
type: `[]` or `[:]` with an annotation. Where nothing gives the type, write
it as Go does: `[]int64{1, 2}`, `map[string]any{"status": "ok", "count": 3}`.

`x in c` tests membership and `!in` negates it: an element of a list
(`slices.Contains`), a key of a map (`_, ok := m[k]`), a substring
(`strings.Contains`), or a number in a range (`code in 200..299`, two
comparisons).

## Loops

```kotlin
func fib(n: int): int {
    a, b := 0, 1
    for _ in 0..<n {
        a, b = b, a + b
    }
    return a
}

func main() {
    println(fib(10))
    grid := [[1, 2], [3, 4], [5, 6]]
    outer@ for i, row in grid {
        for v in row {
            if v == 4 {
                println("found 4 in row", i)
                break@outer
            }
        }
    }
}
```

`for` iterates over a collection, a channel, a range, or a Go iterator
(`for k in maps.Keys(ages)`), and `while c { }`
loops on a condition (`while true` forever). As in Go, conditions take no
parentheses and bodies are always blocks. `a, b := 0, 1` declares several
variables, and an assignment list evaluates all values on the right before
assigning any, so `a, b = b, a + b` needs no temporary. `_` discards a loop
variable. `outer@` labels a loop, and
`break@outer` or `continue@outer` jumps out of the loops inside it; both become
Go labels.

## Lambdas

```kotlin
import "slices"
import "strings"

func mapAll(xs: []int, f: (int) -> int): []int {
    out: []int = []
    for x in xs {
        out = append(out, f(x))
    }
    return out
}

func main() {
    println(mapAll([1, 2, 3], { x -> x * 10 }))
    println(mapAll([1, 2, 3]) { it + 1 })
    words := ["kiwi", "fig", "banana"]
    slices.SortFunc(words, { a, b -> len(a) - len(b) })
    println(strings.Join(words, ","))
}
```

A lambda is `{ params -> body }`. Parameter types come from where the lambda is
used, including generic Go functions such as `slices.SortFunc`. A lambda with one
parameter can leave it unnamed and use `it`, and a lambda that is the last
argument can follow the parentheses. Lambdas are Go closures.

## `if` and `when` are expressions

```kotlin
func size(n: int): string = if n < 10 { "small" } else { "large" }

func describe(code: int): string = when code {
    200, 204 -> "ok"
    404 -> "not found"
    in 500..599 -> "server error"
    else -> "status ${code}"
}

func sign(n: int): string = when {
    n < 0 -> "negative"
    n == 0 -> "zero"
    else -> "positive"
}

func main() {
    println(size(3), size(30))
    println(describe(204), describe(404), describe(503), describe(302))
    println(sign(-2), sign(0), sign(7))
}
```

As in Go, conditions and subjects take no parentheses and bodies are always
blocks, so an `if` used as a value is `if c { a } else { b }`. A condition
cannot end in a trailing lambda; parenthesize it:
`if (xs.Any { it > 0 }) { ... }`. `when` with a subject compares it with each
condition, or tests it with `is` and `in`; without one, each condition is a
boolean. Branches never fall through. Used as a value, both forms need an
`else`. When the value is returned, the generated Go returns from each branch.

## Null safety

```kotlin
struct User {
    Name: string
    Email: string?
}

func find(id: int): User? {
    when id {
        1 -> return User(Name = "Ada", Email = "ada@example.com")
        2 -> return User(Name = "Linus", Email = null)
    }
    return null
}

func main() {
    for id in 1..3 {
        user := find(id)
        if user == null {
            println(id, "nobody")
            continue
        }
        // here user is a User, not a User?
        println(id, user.Name, user.Email ?: "no email")
    }
    println(find(2)?.Name, find(3)?.Name)
}
```

A type with `?` can be null; others cannot, so `user.Name` on a `User?` is a
compile error until a check proves `user` is not null (a *smart cast*). Smart
casts apply to parameters and to local variables the function never
reassigns; for a field or a reassigned variable, copy the value into a new
local first. `?.` reads through a possible null, `?:` supplies a default or
exits early (`?: return`), and `!!` asserts. In Go, `User?` is `*User` and
`string?` is `*string`, which `encoding/json` reads as `null` and omits with
`omitempty`. Values from Go APIs have unknown nullability; see
[Go interop](go-interop.md#nil-and-platform-types).

## Errors

```kotlin
import "errors"
import "strconv"

var ErrNegative = errors.New("negative quantity")

func parseQuantity(s: string): int throws {
    n := strconv.Atoi(s)?
    if n < 0 {
        throw ErrNegative
    }
    return n
}

func total(items: []string): int throws {
    sum := 0
    for item in items {
        sum += parseQuantity(item)?
    }
    return sum
}

func main() {
    println(total(["1", "2", "3"]) catch { -1 })
    n := total(["4", "x"]) catch (err) {
        println("error:", err)
        0
    }
    println(n)
    total(["-5"]) catch (err) {
        println("negative?", errors.Is(err, ErrNegative))
    }
}
```

A function that can fail is declared `throws`; in Go it returns an extra
`error`. Postfix `?` returns the error to the caller, `throw` returns a new one,
and `catch` handles it in place, either with a fallback value or with any code.
Go functions returning `(T, error)` work the same way, so `strconv.Atoi(s)?` is
ordinary Lyth. Ignoring an error silently is a compile error. Errors are Go
`error` values, so `errors.Is`, `errors.As`, and wrapping work unchanged:

```kotlin
import "os"

func readConfig(path: string): string throws {
    data := os.ReadFile(path) catch (err) { throw err.wrap("reading config ${path}") }
    return string(data)
}

func main() throws {
    println(readConfig("missing.toml")?)
}
```

`err.wrap("...")` becomes `fmt.Errorf("...: %w", err)`. A `main` that throws
prints the error and exits with status 1. To handle one type of error and pass
on the rest, `catch (e: *NotFound) { ... }?` matches like `errors.AsType`
([an example](lyth-for-go-programmers.md#handling-one-kind-of-error)). The
details are in [errors](../spec/errors.md).

## Interfaces and type tests

```kotlin
interface Shape {
    Area(): float64
}

struct Circle : Shape {
    R: float64
}

func Circle.Area(): float64 = 3 * R * R

struct Rect {
    W: float64
    H: float64
}

func Rect.Area(): float64 = W * H

func describe(s: Shape): string = when s {
    is Circle -> "circle of radius ${s.R}"
    is Rect -> "${s.W}x${s.H} rectangle"
    else -> "some shape"
}

func main() {
    shapes: []Shape = [Circle(R = 1), Rect(W = 2, H = 3)]
    for s in shapes {
        println(describe(s), s.Area())
    }
    first := shapes[0] as? Rect
    println(first == null)
}
```

Interfaces are structural, exactly as in Go: a type implements one by having
its methods, with the same names (`Area` here, capitalized because the
interface's method is exported).
`struct Circle : Shape` declares the intent. The compiler then checks the
methods at the struct, and the Go code gets the usual
`var _ Shape = Circle{}` assertion; without the declaration, a missing method
shows up where a `Circle` is first used as a `Shape`.
`is` tests a dynamic type and smart-casts, so `s.R` works inside the branch; a
`when` of `is` tests becomes a Go type switch. `as` casts (panicking on failure)
and `as?` yields null instead.

## Enums and sealed interfaces

```kotlin
enum Suit { Hearts, Diamonds, Clubs, Spades }

func Suit.color(): string = when self {
    Hearts, Diamonds -> "red"
    Clubs, Spades -> "black"
}

sealed interface Payment {
    Cents(): int
}

struct Card : Payment {
    Last4: string
    Amount: int
}

func Card.Cents(): int = Amount

struct Voucher : Payment {
    Code: string
}

func Voucher.Cents(): int = 500

func receipt(p: Payment): string = when p {
    is Card -> "card ending ${p.Last4}"
    is Voucher -> "voucher ${p.Code}"
}

func main() {
    println(Suit.Spades, Suit.Spades.color())
    payments: []Payment = [Card(Last4 = "4242", Amount = 1999), Voucher(Code = "WELCOME")]
    for p in payments {
        println(receipt(p), p.Cents())
    }
}
```

An `enum` is a closed set of values. It compiles to Go's `iota` constants
(`SuitHearts`, `SuitSpades`, ...) and a `String` method, so it prints by name.
A `sealed interface` is a closed set of types: only structs of the same
package that declare it (`struct Card : Payment`) implement it.

The point of both is the exhaustive `when`. A `when` over an enum or a sealed
interface must handle every entry or member type, or have an `else`; it then
needs no `else` to be used as a value. Add an entry to `Suit` or a member to
`Payment`, and every `when` that does not handle it stops compiling, which is
the check a Go `switch` cannot give. Inside a `when` over an enum, entries may
be written without the type (`Hearts`).

## Generics and extension functions

```kotlin
import "cmp"
import "lyth/coll"

func maxOf[T: cmp.Ordered](a: T, b: T): T = if a > b { a } else { b }

struct Page[T] {
    Items: []T
    Total: int
}

func Page[T].Empty(): bool = len(Items) == 0

func string.Shout(): string = self + "!"

struct Todo {
    Title: string
    Done: bool
}

func main() {
    println(maxOf(3, 7), maxOf("go", "lyth"))
    todos := [Todo(Title = "spec", Done = true), Todo(Title = "ship", Done = false)]
    open := Page(Items = todos.Filter { !it.Done }.Map { it.Title }, Total = len(todos))
    println(open.Items, open.Empty(), "done".Shout())
}
```

Type parameters go in square brackets after the name, as in Go, with a colon
before the constraint: `func maxOf[T: cmp.Ordered]` is Go's
`func maxOf[T cmp.Ordered]`, and `[T]` alone means `[T: any]`. A constraint is
an interface such as `comparable`, `cmp.Ordered`, or `fmt.Stringer`. A generic
type's methods name its parameters in the receiver (`func Page[T].Empty()`).
Type arguments are inferred, here from the arguments and from the fields given
to `Page(...)`, or written out: `Page[string](...)`, `maxOf[int](3, 7)`.

`func string.Shout()` is an extension function: called like a method, it is a
plain Go function taking the receiver first, `Shout("done")`, and its body
reaches the receiver through `self`. The standard library's `lyth/coll`
provides collection operations this way (`Filter`, `Map`, `GroupBy`,
`SortedBy`, `Take`, ...). Each returns a new slice and never changes its
receiver, so a loop is still the cheapest choice on a hot path; the
[Collection API](../design/collections.md) lists them with their costs.

## Goroutines

```kotlin
import "sync"

func main() {
    wg := sync.WaitGroup()
    mu := sync.Mutex()
    total := 0
    for i in 1..10 {
        wg.Add(1)
        go {
            defer wg.Done()
            mu.Lock()
            total += i
            mu.Unlock()
        }
    }
    wg.Wait()
    println(total)
}
```

`go` and `defer` take a call or a block. Concurrency is Go's: goroutines,
`sync`, `context`, and `errgroup` all work directly.

## Channels

```kotlin
import "time"

struct Job {
    ID: int
}

func produce(jobs: chan<- *Job, n: int) {
    defer jobs.close()
    for i in 1..n {
        jobs.send(&Job(ID = i))
    }
}

func main() {
    jobs := make(chan *Job, 2)
    go { produce(jobs, 3) }
    while true {
        job := jobs.receive() ?: break
        println("job", job.ID)
    }

    results := make(chan string)
    go {
        time.Sleep(time.Second)
        results.send("slow answer")
    }
    select {
        r := results.receive() -> println(r)
        time.After(10 * time.Millisecond).receive() -> println("gave up waiting")
    }
}
```

Channels are Go channels with method syntax: `make(chan T, n)` creates one,
`ch.send(v)`, `ch.receive()`, and `ch.close()` lower to `ch <- v`, `<-ch`, and
`close(ch)`, and `for v in ch` receives until the channel is closed.
`chan<- T` and `<-chan T` restrict a parameter to one direction.
Semantics are Go's: receiving from a closed channel does not fail, it yields
the zero value. That is why `receive()` on a channel of
pointers is nullable, and why `?: break` ends the loop once `produce` closes
`jobs`. `v, ok := ch.receive()` tells a zero value from a closed channel.

`select` waits for whichever channel operation can proceed first. Its branches
look like `when` branches: a receive can bind its value, `else` runs when no
operation is ready, and `break` exits the enclosing loop.

## Where to go next

- [Lyth for Go programmers](lyth-for-go-programmers.md): side-by-side comparisons
  and an honest account of what Lyth simplifies and what it doesn't.
- [Go interop](go-interop.md): using any Go package, and Lyth packages from Go.
- [Testing](testing.md) and [tooling](tooling.md).
- [The REST API example](../../examples/todoapi/README.md): a complete service
  on PostgreSQL.
