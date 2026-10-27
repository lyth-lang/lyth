# Lyth Type System

As in [language.md](language.md), **[planned]** marks what is designed but not
implemented yet; everything else is implemented and tested.

## 1. Two layers: Lyth types and Go representation types

Every Lyth type has a **Go representation type**, computed by a total function
`lower: LythType → go/types.Type`. Lyth types carry extra static information that
Go types cannot express:

- nullability (`*User` vs `*User?`),
- fallibility of function types (`(A) -> R throws`),
- the difference between an optional value (`int?`) and a pointer (`*int`).

Types that come from Go packages enter Lyth through `lift: go/types.Type →
LythType`, which marks every nil-able position as a **platform type** (unknown
nullability, §5.4). For any Go type `t`, `lower(lift(t))` is identical to `t`.
Lifting never simplifies or reinterprets a Go type. This is the formal
statement of the rule that Lyth never changes the meaning of imported APIs.

Go-level relationships (method sets, interface satisfaction, comparability,
conversion rules, generic constraint satisfaction) are decided by `go/types`
on the lowered types, using a parallel *shadow* `go/types` package built for
each Lyth package. Lyth's checker adds its own rules (nullability, mutability,
fallibility) on top. Delegating this way keeps Lyth's model exactly Go's model
wherever Go has an opinion.

## 2. Type syntax

```ebnf
Type        = PrefixType [ "?" ] .
PrefixType  = "*" PrefixType | "[" [ ConstExpr ] "]" Type
            | "map" "[" Type "]" Type | ChanType | PrimaryType .
ChanType    = "chan" Type | "chan" "<-" Type | "<-" "chan" Type .
PrimaryType = TypeName [ TypeArgs ] | FuncType | "(" Type ")" | "struct" "{" "}" .
TypeName    = identifier [ "." identifier ] .
TypeArgs    = "[" Type { "," Type } "]" .
FuncType    = "(" [ Type { "," Type } ] ")" "->" ResultType [ "throws" ] .
```

Types are spelled as in Go: `int`, `string`, `[]T`, `map[K]V`, `chan T`,
`chan<- T`, `<-chan T`, and type arguments in brackets directly after the
name (`Page[int]`, `atomic.Pointer[Config]`). `map` and `chan` are keywords.
The old spellings (`Int`, `Map<K, V>`, `Chan<T>`, `List<T>`, `<T>` type
arguments) are errors (E0101, E0008) whose message names the Go spelling.

The postfix `?` applies to the whole type it follows, so `*User?` is a nullable
pointer `(*User)?`, the overwhelmingly common case in Go interop. Inside type
arguments the scope is explicit. After the element type of a slice, array,
map, or channel, `?` belongs to the element: `[]User?` is a slice of nullable
users and `map[string]*User?` a map of nullable pointers, because slices and
maps are never nullable (a nullable slice would be written `([]User)?`, which
is rejected, §5.1). A nullable channel is `(chan T)?`. A pointer to a
nullable value needs parentheses: `*(User?)`. A nullable function type is
`((A) -> R)?`.

## 3. Basic types and constants

Basic types use Go's names and are listed in
[language.md §8.1](language.md#81-types). `byte` is
identical to `uint8` and `rune` to `int32`, as in Go.

Literals and constant expressions are **untyped constants** with Go semantics:
arbitrary precision, implicit conversion to the target type if representable,
default types `int`, `float64`, `rune`, `string`, `bool`. `x := 1 << 40` has
type `int`. `y: int8 = 300` is E0215 ("constant 300 cannot be represented as
int8").

There are **no implicit conversions between numeric types**: `price * quantity`
with `float64` and `int` operands is E0202, and the hint suggests
`price * float64(quantity)`. This keeps Go's guarantee that every numeric
conversion is visible.

`unit` names the empty result in function types (`(int) -> unit`). It has no
values. `nothing` is the type of diverging expressions (`return`, `throw`,
`break`, `continue`, `panic(...)`, `!!` on `null`). It is a subtype of every type
and appears only in diagnostics; it cannot be written in source.

## 4. Composite types

| Lyth | Meaning |
|---|---|
| `[]T` | Go slice. Indexing, slicing `xs[a:b]`, `len`, `append` as in Go; `[]byte(s)` and `make([]T, n, c)` take the type as an operand ([ADR 0023](../adr/0023-slice-types.md)). |
| `map[K]V` | Go map. `m[k]` yields the zero value for absent keys if `V` has a zero value, otherwise `V?` (§6). `v, ok := m[k]` gives comma-ok. |
| `[N]T` | Go array (value type). |
| `chan T`, `chan<- T`, `<-chan T` | Go channels, made with `make(chan T, n)`. Operations are the methods `send`, `receive`, and `close`; `receive()` is `T?` when `T` has no zero value ([language.md §5.9](language.md#59-pointers-and-channels)). |
| `*T` | Go pointer. |
| `struct{}` | Go's empty struct, for values that carry no data: `chan struct{}`, sets as `map[K]struct{}`. Its value is `struct{}{}`. Other anonymous struct types are not supported; declare a struct. |
| `(A, B) -> R` | Go `func(A, B) R`. |
| `(A) -> R throws` | Go `func(A) (R, error)`. |
| tuple `(A, B)` | multiple results only; not a first-class type. |
| named types | structs, interfaces, and Go defined types such as `time.Duration`, with type arguments for generics. |

## 5. Nullability

### 5.1 Which types can be null

A type is **nil-able** if Go allows `nil` for it: pointers, interfaces,
functions, channels, slices, and maps. Lyth applies nullability to the first four:

- `*T`, interfaces (`error`, `any`, `io.Reader`, ...), function types, and
  channels are **non-null by default**. `T?` is their nullable form. Both lower
  to the same Go type.
- `[]T` and `map[K]V` are never null in Lyth's type system. Go's nil slice
  and nil map are simply their empty zero values: `len` is 0, iteration does
  nothing, reads succeed. `([]T)?` is E0406 with a hint to use an empty slice.
  (Writing to a nil map panics in Go and in Lyth. Map literals and `make` always
  produce non-nil maps.) Go named slice and map types (`http.Header`,
  `json.RawMessage`) follow the same rule. `xs == null` tests for the nil
  slice or map, and `xs ?: fallback` uses the fallback when it is nil:
  `m[k] ?: ["default"]`.
- Value types (numbers, `string`, `bool`, structs, arrays) have optional forms
  `T?` represented as `*T`: see §5.6.

### 5.2 `null`

`null` has a special type of its own (printed `null` in diagnostics),
assignable to any nullable or platform type and to slices and maps (as Go's
nil). It lowers to `nil`.

### 5.3 Using nullable values

A value of type `T?` cannot be used where `T` is required (E0401). Not allowed
on `T?`: member access with `.`, calls, dereference, indexing, and passing as a
non-null argument. The ways to use it:

| Form | Meaning | Lowers to |
|---|---|---|
| `if x != null { ...x... }` | smart cast to `T` in the guarded region | plain `if x != nil` |
| `x ?: default` | `x` if non-null, else `default` (lazy) | `if`/`else` assignment |
| `x?.member` | null if `x` is null, else the member (result nullable) | `if x != nil` |
| `x!!` | assert non-null; panics with source position if null | explicit check + panic |
| `x ?: return/throw/continue/break` | early exit, then `x` is smart cast | `if x == nil { return ... }` |

**Smart casts.** After a condition establishes `x != null`, `x` has type `T`
inside the region the condition dominates. The same holds after an early exit
on `x == null` for the rest of the block. Conditions combine through `&&`, `||`,
and `!`.

A smart cast needs a value that cannot change between the check and the use
([ADR 0046](../adr/0046-smart-casts-between-assignments.md)):

- Parameters, `self`, and locals the function never reassigns smart-cast
  wherever the check holds, also inside lambdas. A local is never reassigned
  when its name is never the target of `=`, a compound assignment, `++`/`--`,
  or `&`, and is never reused by a later `:=` list (language.md §4.2), anywhere
  in the function body, including its lambdas. The analysis is syntactic and
  by name, so it is conservative.
- A reassigned local smart-casts from the check until its next assignment.
  An assignment of a value that is never null smart-casts it to non-null, as
  does a declaration `x: T? = e` with a non-null `e`. A loop ends, at its
  start, the smart casts of the variables its body assigns. After an
  `if`/`else` or a `when`, a variable stays smart-cast when every branch that
  continues leaves it non-null (an `if` without `else`, and a `when` without
  one, include the path that runs no branch):

  ```kotlin
  u := lookup(id)
  if u == null {
      u = guest
  }
  println(u.Name) // u is not null on either path
  ```

  This needs every assignment in view, so it does not apply to a local whose
  address is taken (`&x`), that a lambda, `go` block, or `defer` block
  assigns, or that an `if`, `when`, or `catch` used as a value assigns (such a
  block runs before the plain reads of the expression around it,
  language.md §6); and a smart cast of a reassigned local made outside a
  lambda does not hold inside it.
- Package-level variables (any code in the package may assign them), fields,
  and calls never smart-cast.

E0401 says why a check did not hold and, where it helps, suggests a copy
that can be checked (`curNow := cur; if curNow != null { ... }`); for a field,
copy it into a local first. Comparing a smart-cast variable with `null` warns
(W0004) and compares the variable itself. Type tests (`x is T`, language.md
§5.10) smart-cast parameters, `self`, and locals the function never
reassigns, from an interface type to `T`.

### 5.4 Platform types

Values whose types come from Go have *platform nullability*, written `T!` in
diagnostics (never in source). A platform value can be used as `T` (member
access, passing it on) and as `T?` (compared with null, `?.`, `?:`). Lyth inserts
no runtime checks for platform values: using a nil platform pointer panics
exactly where the equivalent Go would. Kotlin made the same trade-off for Java
interop. Without it every Go call would need `!!`, and the alternative of
treating every Go pointer as nullable is unusable in practice.

Platform-ness is deep: a Go `[]*User` lifts to `[]*User!`. Declaring a
variable with an explicit type removes it: `u: *User = goFind()` commits to
non-null, `u: *User? = goFind()` to nullable. An inferred one (`u := goFind()`)
keeps the platform type.

### 5.5 Nullability and errors are separate

`?` handles errors only and `?.`, `?:`, `!!` handle null only. A missing value is
not an error. A function that can both fail and find nothing returns `T? throws`
(Go: `(*T, error)`), and callers write `find(id)? ?: return notFound()`. See
[errors.md §3.4](errors.md#34-errors-versus-null).

### 5.6 Optional values (`int?`, `string?`, `User?`)

For a value type `T`, `T?` is an *immutable optional value* represented as `*T`.
This is the idiomatic Go representation for optional JSON fields (`*int` with
`null`), nullable SQL columns (`database/sql` and pgx scan NULL into `*T`), and
"maybe a struct" results (`(*User, error)`).

- Boxing `T → T?` lowers to Go 1.26 `new(expr)`, which allocates unless escape
  analysis proves otherwise. Unboxing (a smart cast or `!!`) copies the value out
  (`*p`). Optional values are never mutated through the pointer, so sharing the
  box is unobservable from Lyth: assigning a field of a smart-cast optional
  (`o.X = 1` for `o: P?`) is E0502, and calling a pointer method on one
  (`o.Inc()`, `o?.Inc()`) is E0504. Change a copy and store it back. For the
  same reason a smart-cast optional stored as `T?` again (`return pct` in
  `if pct != null { ... }`) is the original pointer, not a new box.
- `*T?` (nullable pointer) and `T?` (optional value) are different Lyth types
  with the same Go type. `*T`/`*T?` converts implicitly to `T?`, which is a
  read-only view and safe. The reverse is rejected (E0201): it would allow writes
  into a shared box. Write `new(x!!)` for an explicit copy.
- Equality `a == b` on two `T?` values compares contents with null handling, not
  pointer identity. Go would compare a struct or array that holds a `T?` by the
  boxes' addresses, so `==` and `in` on one is E0209: compare the fields you need.
- Converting a nullable value to a nullable interface type (`error?`, `any?`,
  `Shape?`) maps null to a nil interface, not to a non-nil interface holding a
  nil pointer (Go's typed-nil trap), and puts an optional value in unboxed: an
  `int?` converted to `any?` holds an `int`. This covers optional values,
  nullable pointers, functions, and channels, and Go values of unknown
  nullability (`*T!`). The conversion costs one nil check
  (`var e error; if p != nil { e = p }`; a returned value becomes
  `if p == nil { return nil }`), see
  [ADR 0026](../adr/0026-null-is-a-nil-interface.md). Go parameters of
  interface type (`any`, `...any`) get the same conversion
  ([ADR 0040](../adr/0040-optionals-into-any.md)): `fmt.Println(user.Email)`
  prints the email, not a pointer, and drivers read the nil
  interface as NULL. A Go parameter of pointer type receives the Go
  representation, as [ADR 0017](../adr/0017-nullable-values-into-go.md)
  specifies. An assignment list from one call cannot check its values, so it
  rejects such a conversion (E0201).
- `T?` where `T` is a type parameter is not supported (§10): it would need a
  uniform representation that does not depend on the instantiation.
- Go code can write through a pointer Lyth hands it, and Lyth cannot see
  that. `json.Unmarshal(data, &u)` decodes into the existing box of an
  optional field of `u`, which a copy of `u` made before shares (`u := base`
  copies the pointer). Decode into a fresh value, or one whose optional fields
  are null, when another copy must keep its values.

Performance note: optional values cost a pointer indirection and possibly an
allocation, like `*T` in hand-written Go. Hot paths can use `(T, bool)` results
or plain zero values instead.

## 6. Zero values

Go gives every type a zero value. In Lyth a type is **zeroable** when its Go zero
value is a valid Lyth value: numbers, `string`, `bool`, nullable and platform
types, slices, maps, arrays of zeroable types, structs whose fields are all
zeroable, and enums. Non-null pointers, interfaces, functions, channels, and
string-backed enums without a `""` entry are not zeroable.

Rules that follow:

- A local `x: T` without initializer requires zeroable `T` (E0405).
- Struct construction may omit only zeroable fields.
- `make([]T, n)` with `n != 0` requires zeroable `T`, and so do `clear(xs)` on
  a slice and an array literal that gives fewer elements than the length
  (`[3]*User{u}`).
- `m[k]` on `map[K]V` has type `V` if `V` is zeroable, else `V?`, null when
  the key is missing; `v, ok := m[k]` gives `v` the same type. For a struct or
  enum `V`, the read is boxed (`var v *Job; if x, ok := m[k]; ok { v = &x }`),
  and `v, ok = m[k]` cannot assign an existing `v` (E0405): assign `v = m[k]`.
  `ch.receive()` follows the same rule (language.md §5.9).
- Destructuring a fallible call yields the non-error values with their zero
  value on error, so non-zeroable value types become nullable there
  ([errors.md §5](errors.md#5-raw-access-destructuring)).

One gap remains: Go lets `s[lo:hi]` extend a slice up to its capacity, and the
elements past its length are zero values. For a slice of non-zeroable
elements (`make([]*User, 0, 8)[:2]`) those are nils that the type says cannot
exist. Lyth does not check `hi` against `len(s)` at run time; grow such slices
with `append`.

## 7. Mutability

### 7.1 Bindings

Local variables (declared with `:=` or `name: Type`) and package-level `var`s
are mutable, as in Go. Parameters, `for` loop variables, `catch` bindings, and
`self` are **read-only bindings**: assigning one is E0501. A `:=` may declare
a new local of the same type over a parameter, as in
`ctx, cancel := context.WithTimeout(ctx, d)` (language.md §3.2). A value receiver's `self` is a copy of the caller's
value and is read-only in the same way. Constants are not variables;
assigning one is E0501 too.

Read-only means *the value stored in the binding never changes*. For value
types this includes every field and array element reachable without going
through a pointer, the semantics of Swift's `let`. It stops at references:
the target of a pointer, the backing array of a slice, the contents of a map, and
the state behind an interface or channel all belong to other storage. To
change a parameter's value, copy it into a local.

```kotlin
func update(u: User, p: *User, xs: []int) {
    u.Name = "b"            // E0502: cannot assign to u.Name: u is an immutable parameter
    p.Name = "b"            // ok: mutation through a pointer
    p = &User()             // E0501: cannot assign to parameter p: parameters are read-only
    xs[0] = 9               // ok: the backing array is not part of the binding
    xs = append(xs, 4)      // E0501
    v := u                  // a local copy is mutable
    v.Name = "b"            // ok
}
```

### 7.2 Mutable paths

An expression is a **mutable path** if it is a variable (a local or a
package-level `var`), `*p` for any
pointer `p`, a field selection `x.f` where `x` is a mutable path or a pointer, an
index `a[i]` where `a` is a slice, a map (assignment only), or an array that is a
mutable path. Assignments, `++`/`--`, and compound assignments require a mutable
path.

### 7.3 Addresses and pointer receivers

- `&x` requires `x` to be a mutable path (E0503), or a construction `&T(...)`.
  `json.Unmarshal(data, &v)` works on a local `v: T`; the address of a
  parameter is E0503, with a hint to copy it into a local.
- Calling a pointer-receiver method on an addressable value implicitly takes its
  address (Go). Lyth allows it only on mutable paths (E0504): `mu.Lock()` on a
  local `mu: sync.Mutex` works, and on a `sync.Mutex` parameter it is an
  error. Pass a `*sync.Mutex` instead.
- Inside a method with a value receiver, `self` is read-only, so the classic Go
  bug of assigning to a copy's field is caught at compile time (E0502).

Mutability is a compile-time property of Lyth bindings only. It adds no runtime
cost and changes nothing in generated code. Go code receiving a pointer can of
course mutate through it.

## 8. Assignability and conversions

A value of Lyth type `S` is assignable to Lyth type `D` if one of these holds:

1. `S` and `D` are identical.
2. `S` is an untyped constant representable in `D` (Go rules).
3. `S` is the type of `null` and `D` is nullable, platform, a slice, or a map.
4. `S` is `nothing`.
5. `D` is `T?` and `S` is assignable to `T` (implicit boxing for value types,
   no-op for nil-able types).
6. `S` or `D` is a platform type and the types are assignable once nullability
   is ignored on the platform side. In particular, a nullable `T?` may be passed
   where a platform `U!` is expected (a Go parameter) when `T` is assignable to
   `U`. When `U` is an interface it converts as for `U?`: the value, or a nil
   interface when null ([ADR 0040](../adr/0040-optionals-into-any.md));
   otherwise it goes in its Go representation, as a nullable pointer passed
   to a pointer parameter ([ADR 0017](../adr/0017-nullable-values-into-go.md)).
7. `S` is `*T` or `*T?` and `D` is `T?` for a value type `T` (read-only view).
8. Nullability is compatible (non-null → anything, nullable → nullable only) and
   `lower(S)` is assignable to `lower(D)` under Go's rules, for example
   interface satisfaction or channel direction. Go's assignability is decided by
   `go/types` on the shadow types.

Function types are assignable when their lowered Go signatures are identical and
nullability is compatible position by position. A Lyth `(A) -> R throws` is
assignable to and from a Go `func(A) (R, error)`.

Explicit conversions `T(x)` follow Go's conversion rules on the lowered types,
including `(*T)(p)` for a pointer type and `unsafe.Pointer(&x)`. Lyth
additionally rejects conversions that would discard a nullability guarantee
without a check. `copy(dst, src)` needs element types that are assignable
without a conversion: a `[]*User?` cannot be copied into a `[]*User`.

**Comparison.** `==` and `!=` require operands of identical type (ignoring
nullability), with two Go exceptions: a channel compares with a directional
view of it (`chan int` and `chan<- int`), and a value may be compared with an interface
value whose type it implements (`x == "zero"` for `x: any` is true when `x`
holds the string `"zero"`). An untyped constant compared with an interface takes
its default type. Both operands must be comparable.

## 9. Type inference

Lyth uses local, bidirectional inference. There is no global inference across
function boundaries: an exported function's signature is always written out, so a
package's API is explicit and stable.

- **Declarations**: `x := e` gives `x` the type of `e` (untyped constants take
  their default type, platform types stay platform).
- **Expected types flow inward**: into initializers with annotations, arguments,
  return values, collection-literal elements, struct-construction fields, `if`/
  `when` branches, `?:` operands, and lambda bodies. This is how `[]`, `null`,
  untyped constants, and lambda parameter types get their types.
- **Lambdas**: parameter types come from the expected function type. Without one,
  annotations are required (E0208). The result type is the expected result, or
  inferred from the body's last expression.
- **Branches**: the type of `if`/`when`/`?:` is the expected type if there is one,
  otherwise the common type of the branches: identical types, `T` and `null` →
  `T?`, `T` and `nothing` → `T`, untyped constants unified first. If none exists,
  E0208.
- **Expression-bodied functions**: an unexported function `func f(...) = e` without
  a result type gets the type of `e` (untyped constants take their default
  type, as for `:=`). It is inferred on demand, when a call or another body
  first needs it, so declaration order does not matter. A result that depends
  on itself (`func f(n: int) = if n == 0 { 0 } else { f(n - 1) }`), a fallible body,
  and a body yielding several values must declare the result (E0208), and so
  must every exported function ([language.md §3.2](language.md#32-functions)).
- **Generic calls**: type arguments of generic Go functions are inferred by
  unification of parameter types with argument types, then with the expected
  result type. Explicit type arguments are given in full
  (`errors.AsType[*NotFound](err)`, `pgx.RowToStructByName[User]`, §10).

## 10. Generics

Calling generic Go functions and using instantiated generic Go types
(`slices.Contains(xs, x)`, `atomic.Pointer[Config]`). Instantiation and
constraint satisfaction are delegated to `go/types` (`types.Instantiate` with
validation). Type arguments are inferred from the arguments (and through a
defined function type's underlying signature, as Go does): also from the
annotated parameter types of a lambda argument (`compose({ s: string -> len(s) },
...)`), and, for a parameter of a generic interface type, from the methods of
the argument (`describe(g: Getter[T])` called with a type that has
`Get(): int` gives `T = int`, Go's rule since Go 1.21). They may also be given explicitly
and in full: `errors.AsType[*NotFound](err)` calls, and
`pgx.RowToStructByName[User]` is a function value; both lower to `f[T]`. A
result whose type is a bare type parameter bound to a nil-able type is a
platform type (`errors.AsType[*E]` returns nil when it fails).

The parser reads `x[A]` as an index expression; the checker decides, as
`go/types` does, that it is an instantiation when `x` names a generic function
or type (Lyth or Go), and then reads `A` as a type (`*E`, `pkg.T`, `T?`,
`Page[int]`). `x[A, B]` is always an instantiation.

Declaring generic functions, structs, and interfaces:
`func maxOf[T: cmp.Ordered](a: T, b: T): T`, `struct Page[T] { Items: []T }`,
`interface Container[T] { Get(): T }`. The constraint follows a colon, as in
every Lyth name-and-type pair; Go's `[T cmp.Ordered]` is E0008 with the edit
that inserts it. Generic declarations lower 1:1 to
Go generics, so Lyth inherits Go's implementation model (GC-shape stenciling)
and its limits. See [ADR 0028](../adr/0028-generic-declarations.md).

- **Constraints** are interfaces: `any` (the default: `[T]` means
  `[T: any]`), `comparable`, Go constraints such as `cmp.Ordered`, and Lyth or
  Go method interfaces (`T: fmt.Stringer`). `comparable` and type-set
  interfaces can only constrain (E0216). Satisfaction is Go's, checked by `go/types` at each
  instantiation (E0213). Lyth adds two rules: a type argument that may be
  null (`*Sq?`) cannot satisfy a constraint with methods, which the generic
  code would call on null; and an optional value (`int?`), or a struct or
  array holding one, cannot be a `comparable` type argument or a map key,
  since Go would compare the boxes' addresses.
- **Operations** on a value of type `T` are the ones its constraint allows:
  `==` with `comparable`, `<` and `+` when every type of the constraint's
  type set supports them (`cmp.Ordered`), and the constraint's methods.
- **Inference**: type arguments come from the arguments, the expected result
  type (`z: int = zero()`), and lambda results (`mapAll(xs) { "#${it}" }`
  binds `R` to `string`); the receiver comes first for extension functions.
  A struct's come from its fields at construction. When inference cannot
  bind a type parameter, give all type arguments: `zero[string]()`. The
  generated call names them only where Go cannot infer them.
- **Zero values**: `z: T` is Go's zero value of the type argument, also
  for type arguments without a Lyth zero value (a nil `*User`). Generic code
  returns it only beside a flag or an error, as Go code does:
  `func []T.Find[T](match: (T) -> bool): (T, bool)`.
- **Methods** of a generic struct name its type parameters in the receiver
  (`func Page[T].First(): T`); they cannot add their own (Go has no generic
  methods), which generic extension functions replace. An instance's members
  have the type arguments substituted (`Page[User].Items` is `[]User`).
- **Type sets** may be written in constraints and interfaces, as in Go:
  `[T: int | float64]`, `interface Number { ~int | ~float64 }` (language.md
  §3.4). An untyped constant is assignable to `T` when it fits every type of
  the set (`total: T = 0` for `T: Number`).
- **Named arguments and defaults** work on generic functions too. A default
  of a parameter whose type is a type parameter must be a constant; it takes
  the type the call's other arguments give `T`, and must fit it (E0215):
  `func Clamp[T: int | float64](x: T, lo: T = 0, hi: T = 100): T`.
- **Not supported**: `T?` for a type parameter (E0407), by design for now:
  Go's only uniform representation, `*T`, is a box even where Lyth's `T?`
  is not (`*User?` is a nilable `*User`), so a `T?` instantiated with a
  pointer would change representation at the call. Generic code returns
  `(T, bool)` instead, as Go does.

## 11. Interfaces and method sets

Method sets follow Go: the method set of `T` contains value-receiver methods,
and `*T` contains both. A Lyth type satisfies an interface when its lowered Go
type does (decided by `go/types.Implements`) *and* the Lyth signatures agree on
nullability and fallibility: a method may return a non-null value where the
interface allows null, and accept null where the interface passes a non-null
value, but not the reverse (`Find(): *User?` does not implement
`Find(): *User`). A value used as an interface it does not satisfy
is a type mismatch (E0201) whose note names the missing method and the reason:
missing, wrong signature, pointer receiver, or a name that differs only in case. A struct can declare
the interfaces it implements (`struct Circle : Shape`,
[language.md §3.3](language.md#33-structs)); a declaration that does not
hold is E0212 at the declaration, naming the method and the signature the
interface needs.

## 12. Display of types in diagnostics

Diagnostics always print Lyth types, which use Go's spelling wherever Go has
the type: `int64`, `map[string]int`, `chan int`, `[]User`. Lyth's own forms
print in Lyth: `(string) -> int throws`, not `func(string) (int, error)`;
nullable types as `*User?`; untyped constants as `untyped int`; the types of
`null` and of diverging expressions as `null` and `nothing`. Platform
types print as `*User!` (`error!`) with a one-time explanatory note. Go-package types are
qualified by their package name (`http.Request`). Verbose mode adds the Go
spelling for interop debugging.
