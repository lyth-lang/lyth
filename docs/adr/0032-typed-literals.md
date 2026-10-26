# ADR 0032: Go's typed literals for slices, arrays, and maps

**Status:** Accepted (extends [0031](0031-go-spelling.md))

## Context
A Lyth collection literal, `[a, b]` or `[k: v]`, takes its type from a
declaration or parameter, or else from its elements. Where neither gives the
type, there was no way to write it: a response body
`["status": "ok", "count": 3]` passed to an `any` parameter has no common
value type, `[]any` argument lists and `[]int64` values needed a declared
variable first, and arrays could not be written as literals at all. Go
spells all of these with composite literals: `map[string]any{...}`,
`[]int64{1, 2}`, `[3]int{1, 2, 3}`. Go's struct literal `Point{X: 1}` is a
different matter: Lyth constructs structs by calling the type, with named and
default arguments (ADR 0019).

## Decision
- `[]T{a, b}`, `[N]T{a, b}`, `[...]T{a, b}`, and `map[K]V{k: v}` are typed
  literals with Go's meaning; they lower to the same Go literal. An array
  literal may give fewer elements than its length (the rest are zero), as in
  Go; `[...]T` takes the length from the elements.
- Only these type forms take braces. They cannot be called with a trailing
  lambda or be a struct, so `[]int{` is never ambiguous; a named type
  (`http.Header`, `gin.H`) is converted from a Lyth literal instead:
  `http.Header(["Accept": ["text/plain"]])`. The literal takes the named
  type, so its elements are typed by the type's elements
  (`gin.H(["error": msg, "code": 7])` is a `map[string]any`), and it lowers
  to Go's literal of that type, `gin.H{"error": msg, "code": 7}`. The same
  holds wherever the named type is expected: `h: http.Header = [...]`, an
  argument, a result. Go's `gin.H{"k": v}` is E0008 with the conversion as its
  edit; a parser cannot tell a named map from a struct, and `T {` is a
  trailing lambda, so the braces stay reserved for the type forms above.
- Elements are ordinary expressions, typed by the element type: an inner
  collection is written `[1, 2]` or with its own type, and a struct element
  is a construction call. Go's elided inner literals (`{1, 2}`, `{X: 1}`) are
  E0008, because `{...}` is a lambda in Lyth. Indexed elements (`{0: a}`) are
  E0008.
- ~~As in Go, a typed literal in an `if`, `while`, `for`, or `when` header must
  be parenthesized (`for x in ([]int{1, 2}) { ... }`); without parentheses it
  is E0008 with that edit.~~ Amended (2026-10-26): Go requires the
  parentheses only for a literal written with a type name (`T{...}`), which
  Lyth does not have. A slice, array, or map type is never a header's value,
  so its braces are the literal's, as Go reads them:
  `for x in []int{1, 2} { ... }` needs no parentheses.
- Lyth's inferred literals stay: `[1, 2]`, `["a": 1]`, `[]` and `[:]` where a
  type is expected, and `a: [3]int = [1, 2, 3]`, which must give every
  element. A map literal that gives a constant key twice is E0102 in both
  forms.
- Go's struct literal stays E0008 with the construction call as its edit
  (`Point(X = 1)`, `Page[int](Items = [1])`).

## Alternatives
- **A conversion form, `map[string]any(["status": "ok"])`**: keeps one
  literal syntax, but reads as a conversion of a value that does not exist
  and is not Go's spelling.
- **Keep declarations as the only way to type a literal**: no new syntax, but
  common backend code (`map[string]any` bodies, `[]any` arguments) needs a
  temporary variable.
- **Go's elided inner literals**: shorter nested data, but `{...}` is a
  lambda in Lyth, so an element `{1, 2}` would change meaning with the element
  type.
- **Struct literals `Point{X: 1}`**: a second construction syntax next to
  the call, which supports defaults and positional arguments, and the same
  braces as a trailing lambda after a name.

## Consequences
Typed collection data reads as in Go, and the generated Go is the same
literal. There are two ways to write a slice literal when the type is
already known (`xs: []int = [1, 2]` and `xs := []int{1, 2}`); the guide
recommends the inferred form where a type is already given and the typed
form where it is not. Tests: `run/typedlit`, `gen/typedlit`,
`check/typedlit_neg`, and the parser tests.
