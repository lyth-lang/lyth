# ADR 0047: The Go constructs Lyth lacked

**Status:** Accepted (2026-10-27). Amends [ADR 0031](0031-go-spelling.md)
(`for` loops, `if`, groups) and [ADR 0012](0012-rejected-features.md).

## Context

Porting real Go packages to Lyth (encoding/csv, text/tabwriter, flag,
container/list, container/heap, container/ring) showed where Lyth was not
yet "all of Go": the ports passed Go's own tests, but each had to work
around a Go construct that Lyth did not have, and several workarounds were
worse than the Go they replaced.

- **Three-part `for`.** tabwriter's `format` advances its loop variable in
  the body, heap's `Init` counts down. As `while` loops with the increment
  at the end, a `continue` skips the increment: the loop hangs, a bug Go's
  `for init; cond; post` cannot have.
- **`const ( ... )` with `iota`.** tabwriter's flags are `1 << iota` of type
  `uint`, flag's error handling is a typed `iota` sequence whose Go names
  must stay `ContinueOnError`, not an enum's `ErrorHandlingContinueOnError`.
  Written one by one, the values are typed by hand and the group's doc
  comment, which `go doc` shows, is lost.
- **Named results.** tabwriter's `Write` reports the bytes consumed before
  a recovered panic; the port needed a second function and a pointer.
  `go doc` of every port lost the names (`(n int, err error)`) that document
  Go APIs.
- **Type-set constraints.** `[T int | float64]` and `interface Number {
  ~int | ~float64 }` are how generic numeric code is written since Go 1.18.
- **Opting out of automatic json keys.** A ported API must keep encoding
  `Value` as `"Value"`; the only way was an explicit tag per field.

ADR 0031's principle settles each case: Go's spelling for what Go has, and
Lyth syntax only where it improves on Go. None of these has a Lyth spelling
that improves on Go's. ADR 0031 kept `for` for iteration because `for cond
{ }` would be ambiguous with a membership test (`for x in xs`); a three-part
header, with its two semicolons, is not.

## Decision

1. **Three-part `for`**, as in Go: `for i := 0; i < n; i++ { }`. The init
   and post are simple statements (`i := 0`, `i, j := 0, n - 1`, `i++`,
   `i += 2`, `i, j = i + 1, j - 1`); any part may be empty except that
   `for ; ; { }` is written `while true { }`. The variable is a mutable
   local of each iteration, as in Go 1.22 and later. `for i in 0..<n` stays
   the way to count by one, and both lower to Go's loop.
2. **Groups**, as in Go: `const ( ... )` at package level and in functions,
   with `iota` and implicit repetition (an entry with only a name repeats
   the previous expression and type), and `var ( ... )` at package level.
   Entries use Lyth's `name: Type`: `A = 1`, `A: uint = 1 << iota`, `B`,
   `Default: Config = Config()`, `cache: map[string]int`. A comment above
   the group is its doc. `iota` is the index of the entry in the group (0
   outside a group), as in Go. `enum` stays the declaration of enumerations
   (names on the wire, exhaustive `when`); groups are for flags, sizes, and
   Go-compatible names.
3. **Named results**: `func split(n: int): (q: int, r: int)`, and with
   `throws`, `func *Writer.Write(buf: []byte): (n: int) throws`. The names
   are locals declared with their zero values (so their types need one,
   E0405 otherwise), visible to `defer` blocks, which may assign them. A
   `return` with values assigns and returns them; a bare `return` returns
   them as they are (only in a function with named results). They lower to
   Go's named results; a `throws` function's error result is named too.
4. **Type sets** in constraints and interfaces, as in Go:
   `[T: ~int | ~float64]`, `interface Number { ~int | ~int64 | ~float64 }`.
   An interface with type terms is a constraint only.
5. **`json:""` keeps encoding/json's default key** (the field name): an
   empty tag value is the explicit opt-out of the automatic key, and the Go
   field is left untagged, as the ported Go type was; a tag with options and
   no name (`json:",omitempty"`) keeps the automatic name as before.
6. **`if` with an init statement**, as in Go: `if v, ok := m[k]; ok { }`.
   The names are in scope in every branch of the `if`.
7. **Complex numbers**, as in Go: `complex64`, `complex128`, imaginary
   literals (`2i`), and `complex`, `real`, `imag`.
8. **Generic type aliases**, as in Go 1.24: `type Set[T: comparable] =
   map[T]bool`.
9. **Named arguments and defaults on generic functions.** Type arguments are
   inferred from the arguments given; a default whose type is a type
   parameter must be a constant, and takes the type the call gives `T`.
10. **Parameter names in function types**: `(name: string, depth: int) ->
    bool`, kept in Go's `func(name string, depth int) bool`.
11. **External test packages** (`package foo_test` in a `_test.lyth` file),
    as in Go.
12. **`lyth run file.lyth` builds that file alone**, as `go run file.go`
    does, also inside a module.

Kept as they are:

- **`for cond { }` and `for { }`** stay `while cond { }` and `while true { }`
  (ADR 0031: ambiguous with `for x in xs`).
- **`range`** stays rejected (ADR 0031).
- **Anonymous struct types** other than `struct{}` stay unsupported: a
  struct declared in the function (`struct tc { in: string }`) is one line
  and gives table tests a name that error messages use.
- **`T?` on a type parameter** stays unsupported. Go's only representation
  that works for every instantiation is a box (`*T`), while Lyth's `*User?`
  is a nilable `*User` with no box; a `T?` instantiated with a pointer type
  would need a conversion at every call. Generic code returns `(T, bool)`,
  as Go code does.
- **A package is one language.** Lyth files and hand-written Go non-test
  files do not mix in one package: each side would need the other's
  declarations before its own are checked. Packages of either language
  import each other, so code moves to Lyth a package at a time.
- **No `when` over errors.** A typed `catch` chain is the construct that
  matches errors through wrapping; a `when` of `errors.Is` conditions
  covers sentinel values.

## Consequences

- Code ported from Go keeps its loops, flag groups, and API documentation.
- The diagnostics that rejected three-part loops, const and var groups, and
  named results (E0008 with edits, added in 0.5.0) are removed: the forms
  are valid. `lyth fix` no longer rewrites them; code it rewrote stays
  valid.
- Two ways to count by one exist (`for i in 0..<n` and the three-part
  form), as in Go (`for i := range n` and the three-part form).
- Generated Go for these constructs is Go's own construct, unchanged.

## Implementation

- Parser: `ForStmt` with `Init`, `Cond`, `Post`; `ConstDecl`/`PropDecl`
  groups (`Group` with its doc and the implicit repetition); named results
  in `ResultType.Names`; union and `~T` terms in constraint and interface
  type expressions.
- Checker: `iota` in constant expressions, repetition of the previous
  entry; named results as locals of the function, bare `return`; type sets
  through go/types unions, so operators on type parameters follow Go.
- Lowering: Go's `for`, grouped `GenDecl`s with implicit specs, named
  `FieldList` results, union constraint expressions; `encoding/json` tags
  left as written when empty.
- Tests: `run/goforms`, `check/goforms_neg`, `gen/goforms`, and for the
  build, `TestExternalTestPackage`.
