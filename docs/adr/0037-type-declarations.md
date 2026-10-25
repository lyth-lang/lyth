# ADR 0037: `type` declares defined types and aliases

**Status:** Accepted (2026-10-25). Amends [0031](0031-go-spelling.md)
(`typealias`) and the type-alias section of the spec (language.md §3.6).
Breaking: `typealias X = Y` becomes `type X = Y`.

## Context

Lyth can declare structs, interfaces, enums, and aliases, but not a Go *defined
type* over anything else. Backend Go uses defined types constantly:

```go
type UserID int64            // an ID that cannot be mixed up with an OrderID
type Status string           // constants of a string type, stored as text
type Cents int64             // money with its own methods
type Middleware func(http.Handler) http.Handler
type Set[T comparable] map[T]struct{}
```

Today `type UserID int64` is E0003 ("declare types with 'struct …',
'interface …', or 'typealias …'"). The workarounds lose the point:

- `typealias OrderID = int64` gives no safety; `repo.GetOrder(ctx,
  customer.ID)` compiles.
- A three-line Go package declaring the types works, but a Lyth package cannot
  then be imported by that Go package in the same module (review finding
  3.1.5).

Separately, `typealias` is a different word for Go's `type X = Y`. Design
principle 8 rules that out: "a Lyth spelling … is never a different word for
the same thing".

## Decision

- **Defined types.** `type Name Underlying` declares a defined type with Go's
  meaning, for any underlying type except a struct or interface body:
  - basic types, slices, arrays, maps, channels, function types, and pointers;
  - generic forms such as `type Set[T: comparable] map[T]struct{}`.

  It lowers to the same Go declaration.

  ```kotlin
  type UserID int64
  type Cents int64
  type Role string
  type Handler (http.ResponseWriter, *http.Request) -> unit throws

  func Cents.Euros(): float64 = float64(self) / 100

  const Admin: Role = "admin"
  ```

  - **Methods.** Methods are declared as on structs, except on pointer and
    interface underlying types, where Go forbids them.
  - **Conversions.** Conversions are explicit, as in Go (`UserID(n)`,
    `int64(id)`), and untyped constants are assignable.
  - **Nullability and zero values.** A defined type takes the nullability and
    zero-value rules of its underlying type:
    - over a function, pointer, or channel type it is non-null, and `T?` is the
      nullable form;
    - over a slice or map it is never null (types.md §5.1).
  - **Go named function types.** The same rule applies to them, which closes
    review finding S5: a `context.CancelFunc` field could be left out at
    construction and was then treated as never null.
- **Aliases.** `type Name = Type` declares an alias, Go's spelling.
  `typealias` stays recognized: E0008, with the edit to `type Name = …` marked
  as a fix. Generic aliases (`type Pair[T] = Tuple[T, T]`, Go 1.24) are
  deferred: they are reported as not supported yet, and adding them later is
  not breaking.
- **No `type … struct`.** Structs and interfaces keep their own declarations
  (`struct User { … }`, `interface Store { … }`). `type User struct { … }` is
  E0008, with the edit to `struct User { … }`, because Lyth's body syntax
  (`Name: Type`) differs from Go's and "one way to say it" applies.
- **Keyword.** `type` becomes a keyword. It cannot be a Go identifier either,
  so no Go API is affected.
- **Closed sets stay enums.** A defined type with constants is an open set: a
  `when` over `Role` needs `else`. A closed set with exhaustiveness is still an
  `enum`, and its string-backed form covers the `type Status string` idiom
  ([ADR 0035](0035-enums-on-the-wire.md)).

## Alternatives

- **Keep `typealias` and add `newtype` or `distinct type`.** That is two Lyth
  words for two Go concepts that Go spells with one keyword.
- **`type` for everything, including `type User struct { Name: string }`.**
  It reads as Go at first glance, but the body is not Go, so it is a false
  friend. `struct`/`interface`/`enum` up front stay consistent with each other.
- **Only aliases (status quo).** Typed IDs and string types stay impossible.
  The Go-package workaround is blocked by the Lyth → Go → Lyth import gap.

## Consequences

- **What it unlocks.** Typed IDs, money types, string constants, and named
  function types with methods read as in Go, and the generated Go is
  identical.
- **One declaration keyword per kind.** `struct`, `interface`, `enum`, `type`.
  `typealias` disappears from the language, and `type` takes over aliases.
- **Go named function types become stricter.** Fields of Go named function
  types (`context.CancelFunc`, `http.HandlerFunc`) must be given at
  construction or be declared nullable. This is a new compile error in code
  that compiled before, where it was a run-time panic.

### Migration
- **`typealias`.** `typealias X = Y` stays recognized with a fix, `lyth fix`
  rewrites it, and the guide's version table gains a row.
- **Stricter construction.** The construction check for Go named function
  types is a plain error: its edit (declaring the field `T?`) changes meaning,
  so it is a hint, not a fix.

### Implementation
- **Parser.** Add a `type` declaration with both forms. Remove the E0003 hint
  that mentions `typealias`, and recognize `typealias` and `type … struct` for
  their diagnostics.
- **Checker.** Named types over non-struct underlying types follow the
  existing Go named collection support (`run/named_collections`); methods
  reuse struct method declaration. Zero-value rules come from the underlying
  type.
- **Tests.**
  - `run/definedtypes`: IDs, money, methods, conversions, constants, a
    generic set.
  - `check/definedtypes_neg`: mixing IDs, methods on pointer types, a missing
    function field.
  - `fix/typealias`.
