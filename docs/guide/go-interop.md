# Go interop

Lyth has no foreign-function layer: Lyth code is Go code once compiled, so every
Go package works directly, with its exact Go names and types, and every Lyth
package is an ordinary Go package that Go code can import. This page shows how
Go APIs look from Lyth and how Lyth APIs look from Go. The design is in
[interop](../design/interop.md), and the tested status of each construct is in
the [interop matrix](../design/interop.md#10-compatibility-matrix).

## Packages and names

```kotlin
import "fmt"
import "net/url"
import "strings"
import "crypto/sha256" as sha

func main() {
    u := url.Parse("https://example.com/a?b=1") catch { return }
    fmt.Println(u.Host, u.Query().Get("b"))
    user, domain, found := strings.Cut("ada@example.com", "@")
    println(user, domain, found)
    println(len(sha.Sum256([]byte("lyth"))))
}
```

Imports are Go import paths, resolved by the `go` command against your
`go.mod`, and the qualifier is the package's declared name. `as` renames it.
Go identifiers are used exactly as Go spells them (`u.Host`, `strings.Cut`):
Lyth never translates names in that direction, and a wrong case gets a
suggestion (`strings.cut` → "Go names are used verbatim: write strings.Cut").
Multiple results are declared as in Go: `a, b, c := ...`.

## Results with errors

A Go function whose last result is `error` is *fallible* in Lyth: `?`
propagates the error and `catch` handles it, so `url.Parse(s)?` is the
everyday form. When you want the raw pair, destructure it:
`u, err := url.Parse(s)`. Ignoring the error entirely is a compile error,
except for the conventional cases `errcheck` also exempts (writes to
`os.Stdout`, `strings.Builder`, `bytes.Buffer`, and `fmt.Print*`).

## Go structs and methods

```kotlin
import "fmt"
import "net/http"
import "strings"
import "time"

func main() {
    srv := &http.Server(Addr = ":8080", ReadHeaderTimeout = 5 * time.Second)
    srv.IdleTimeout = time.Minute
    fmt.Println(srv.Addr, srv.ReadHeaderTimeout, srv.IdleTimeout)

    sb := strings.Builder()
    sb.WriteString("built ")
    sb.WriteString("in Lyth")
    println(sb.String(), sb.Len())
}
```

Go structs are constructed with named arguments only, since Go field order is
not part of an API. Exported fields are read and written directly. Methods
with pointer receivers work on locals (Lyth takes the address, as Go does); on
a read-only value such as a parameter they are an error (E0504), because the
method could modify it.

## Named collection types

Go packages define many named slice and map types: `http.Header`,
`url.Values`, `template.FuncMap`, gin's `gin.H`. A Lyth literal written where
one is expected has that type, and its elements are typed by the type's
elements; a conversion gives the type in place:

```kotlin
import "fmt"
import "net/http"
import "net/url"
import "sort"

func accept(h: http.Header): string = h.Get("Accept")

func main() {
    h: http.Header = ["Accept": ["text/plain"]]
    h.Add("Accept", "text/html")
    fmt.Println(h.Values("Accept"), accept(["Accept": ["application/json"]]))

    q := url.Values(["q": ["lyth"], "page": ["2"]])
    fmt.Println(q.Encode())

    names := sort.StringSlice(["carol", "alice", "bob"])
    names.Sort()
    fmt.Println(names)
}
```

Each literal lowers to Go's literal of the named type, such as
`http.Header{"Accept": {"text/plain"}}` and `url.Values{"q": {"lyth"}, ...}`.
In gin, `c.JSON(200, gin.H(["error": msg, "code": 7]))` is
`c.JSON(200, gin.H{"error": msg, "code": 7})`. Go's own spelling
`gin.H{"error": msg}` is an error whose fix is the conversion: braces after a
type name are a trailing lambda in Lyth.

Values of these types index, range, and take `len` like their underlying
slice or map: `h["Accept"]`, `for k, vs in q`, `"q" in q`, `len(names)`.

## Implementing Go interfaces

```kotlin
import "fmt"
import "sort"

struct Temperature {
    Celsius: float64
}

func Temperature.String(): string = "${Celsius}°C"

struct NotFound {
    Key: string
}

func *NotFound.Error(): string = "not found: ${Key}"

struct byLength : sort.Interface {
    Words: []string
}

func byLength.Len(): int = len(Words)
func byLength.Less(i: int, j: int): bool = len(Words[i]) < len(Words[j])
func byLength.Swap(i: int, j: int) {
    Words[i], Words[j] = Words[j], Words[i]
}

func lookup(key: string): int throws {
    throw &NotFound(Key = key)
}

func main() {
    fmt.Println(Temperature(Celsius = 21.5))
    words := ["banana", "fig", "kiwi"]
    sort.Sort(byLength(Words = words))
    println(words)
    lookup("answer") catch (e: *NotFound) {
        println("missing", e.Key)
    } catch {
        println("lookup failed")
    }
}
```

A Lyth type implements a Go interface the Go way, by having its methods with
the interface's exact names (`String`, `Error`, `ServeHTTP`). `fmt.Stringer`,
`error`, `sort.Interface`, `http.Handler`, and `io.Writer` are implemented this
way throughout the examples and tests. Declaring the interface, as in
`struct byLength : sort.Interface`, is optional: it makes the compiler report
a missing or mistyped method at the struct, and generates Go's
`var _ sort.Interface = byLength{}`. Lyth error types work with `errors.Is`,
`errors.AsType`, and typed `catch` clauses, which see through wrapping.

## Generic functions

```kotlin
import "cmp"
import "errors"
import "io/fs"
import "os"
import "slices"

struct Item {
    Name: string
    Price: int
}

func main() {
    items := [Item(Name = "tea", Price = 4), Item(Name = "cake", Price = 7), Item(Name = "jam", Price = 2)]
    slices.SortFunc(items, { a, b -> cmp.Compare(a.Price, b.Price) })
    println(items[0].Name, slices.IndexFunc(items, { it.Name == "cake" }))
    println(slices.Max([3, 9, 4]), slices.Contains(["a", "b"], "b"))

    os.Remove("/no/such/file") catch (err) {
        pe, ok := errors.AsType[*fs.PathError](err)
        println(ok, pe.Op, pe.Path)
    }
}
```

Type arguments of generic Go functions are inferred from the arguments,
including a lambda's parameter types (`a` and `b` are `Item` above). When they
cannot be inferred, give them all explicitly in square brackets, as in Go:
`errors.AsType[*NotFound](err)` calls a generic function, and
`pgx.RowToStructByName[User]` is a generic function value. Generic functions and types declared in Lyth are Go generics too, so Go
code calls them as `pkg.MaxOf(1, 2)` or names them as `pkg.Page[User]`, and a
Lyth extension function is an ordinary Go function taking the receiver first.

## Callbacks and function types

```kotlin
import "fmt"
import "net/http"
import "net/http/httptest"
import "strings"
import "unicode"

typealias Middleware = (http.Handler) -> http.Handler

func withHeader(name: string, value: string): Middleware = { next ->
    http.HandlerFunc({ w, r ->
        w.Header().Set(name, value)
        next.ServeHTTP(w, r)
    })
}

func main() {
    println(strings.Map({ r -> unicode.ToUpper(r) }, "shout"))
    app := withHeader("X-Powered-By", "Lyth")(http.HandlerFunc({ w, r ->
        fmt.Fprint(w, "ok") catch {}
    }))
    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, httptest.NewRequest("GET", "/", null))
    println(rec.Header().Get("X-Powered-By"), rec.Body.String())
}
```

Lambdas are Go closures and can be passed wherever Go expects a function. A
lambda converted to a named Go function type, such as `http.HandlerFunc`,
takes its parameter types from that type. `typealias` names a function type
without creating a new one, which suits handler and middleware types. A Go
callback type that returns an error (`fs.WalkDirFunc`) accepts a lambda that
uses `?`, because Go has no `throws` and a trailing `error` is how it says a
function can fail. A function type written in Lyth declares that with
`throws`; `(int) -> error?` is an ordinary function type whose lambda returns
the error as its value.

## Nil and platform types

Go does not say whether a pointer, interface, map, or function may be nil, so
values that come from Go have *platform types*, written `T!` in diagnostics.
Lyth lets you use them as non-null, and does not insert
checks: a nil dereference panics as it would in Go, with the `.lyth` line in the
stack trace. Declare a local of type `T?` for a Go result (`u: *url.URL? = f()`)
when you want Lyth to make you check it.

In the other direction, a nullable Lyth value may be passed to a Go parameter in
its Go representation: a `string?` goes in as `*string` (nil when null), which
is what `database/sql`, `pgx`, and `encoding/json` expect for optional values.
See [ADR 0017](../adr/0017-nullable-values-into-go.md).

Lyth's own interface types work differently. When a nullable pointer or
optional value becomes an `error?`, `any?`, or other nullable interface in
Lyth code, null becomes a nil interface, never Go's non-nil interface holding a
nil pointer, and an optional value goes in unboxed
([types](../spec/types.md#56-optional-values-int-string-user)).

## Lyth packages seen from Go

```kotlin
import "errors"

struct Account {
    Owner: string
    Balance: int64
    pin: string
}

var ErrInsufficient = errors.New("insufficient funds")

func Open(owner: string): *Account = &Account(Owner = owner, Balance = 0, pin = "0000")

func *Account.Withdraw(amount: int64): int64 throws {
    if amount > Balance {
        throw ErrInsufficient
    }
    Balance -= amount
    return Balance
}
```

The Go tab is the API Go code sees: `Open`, `Account` with exported `Owner` and
`Balance` fields and their `json` tags, an unexported `pin`, and
`Withdraw(amount int64) (int64, error)`. Capitalized names are exported, as in
Go, and every Lyth name is used unchanged as the Go name, so `ID` and `UserID`
are spelled the way Go code expects. An `enum Kind { Round }` is a Go
`type Kind int` with constants `KindRound` and a `String` method, and a
`sealed interface` is an interface Go code can use but, because of its
unexported marker method, not implement. Go code calls Lyth
packages like any other Go package. The REST API example tests its Lyth store
from a plain Go test file ([testing](testing.md#go-test-files-beside-lyth-code)).

## Modules and third-party packages

Dependencies are ordinary Go modules in `go.mod`:

```bash
go get github.com/jackc/pgx/v5
lyth mod tidy
```

Use `lyth mod tidy` instead of `go mod tidy`: the `go` command cannot see
imports inside `.lyth` files and would remove modules only Lyth code uses. The
[REST API example](../../examples/todoapi/README.md) uses `pgx` this way.

## Limits and differences

- A package cannot mix Lyth and Go source files yet, apart from `_test.go`
  files. Put Go code in its own package and import it.
- Go's channel operator is spelled with methods: `ch <- v` is `ch.send(v)` and
  `<-ch` is `ch.receive()` (`<-ctx.Done()` is `ctx.Done().receive()`), and
  `select` has `when`-style branches.
