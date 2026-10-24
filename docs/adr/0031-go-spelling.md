# ADR 0031: Go's spelling for what Go already has

**Status:** Accepted and implemented (supersedes [0007](0007-val-var-and-immutability.md) for locals and the type spellings of [0011](0011-type-syntax.md); amends [0009](0009-nullability.md) and [0016](0016-type-tests.md) (smart casts), [0012](0012-rejected-features.md), [0023](0023-slice-types.md), [0024](0024-channel-operations.md), [0028](0028-generic-declarations.md), [0029](0029-extension-functions.md))

## Context
Lyth's first syntax borrowed Kotlin's spelling wherever Kotlin's semantics
held (design principle 8 as first written). Applied construct by construct,
that left two spelling systems: `[]T` and `[N]T` from Go; `fun`, `val`,
`Int`, `Map<K, V>`, `Chan<T>`, `<T>`, `this`, and parenthesized `if (c)`
from Kotlin. Go programmers, who adopt a language that compiles to Go, paid
for each borrowed spelling and got nothing for it:

- The compiler carried recovery code whose only job was to turn Go into
  Kotlin: `int` was "undefined type int; Lyth spells this type Int", and
  `if c {` was "if conditions must be parenthesized".
- Capitalization is visibility (ADR 0022), so `Int`, `String`, and `Error`
  read as exported declarations.
- Every Go signature from `go doc`, every Go error message, and every
  generated line was spelled differently from the Lyth that produced it.
- `<T>` in expressions needed speculative parsing (64 tokens of lookahead,
  `>>` splitting).

A review compared this design with a much smaller, inference-heavy one
(untyped parameters, `=` that declares, no declaration keywords) and with a
hybrid. Lyth's value is in its semantics (`throws`/`?`/`catch`, nullable
types, expression `if`/`when`, named and default arguments), not in its
Kotlin surface, and inference across functions would cost the checker's
signature-first design. The decisions below were then settled: `func`, `:=`,
`const`, square-bracket generics, Go's type spellings, unparenthesized
headers, and methods declared `func Type.Method` with an implicit `self`.

## Decision

### Principle
Design principle 8 becomes: **use Go's spelling and conventions for
concepts Go already has; reserve Lyth syntax for features that improve on
Go.** A Lyth spelling must name something Go lacks, or change a meaning Go
would get wrong; it is never a different word for the same thing.

### Functions
```kotlin
func Add(a: int, b: int): int {
    return a + b
}

func Double(n: int): int = n * 2
func square(n: int) = n * n         // unexported: the result type may be inferred
func Split(s: string): (string, string) { ... return a, b }
func Parse(s: string): int throws = s.toInt()
```
- `func` declares functions; `fun` is an error with the edit `func`.
- Parameters always have types, written `name: Type`; results follow `:`.
- A block body returns with `return`; there is no implicit return of the
  last expression. One expression may be the body: `= expr`.
- Multiple results are `(A, B)` and are returned as in Go: `return a, b`.

### Methods and `self`
```kotlin
func Counter.Next(): int = self.Count + 1   // value receiver: self is a copy
func *Counter.Inc() { self.Count++ }        // pointer receiver: self points at the caller's value
func Page[T].First(): T = self.Items[0]     // a generic type's parameters
func string.Twice(): string = self + self  // an extension function
```
- A method is declared `func Type.Name(...)` (value receiver) or
  `func *Type.Name(...)` (pointer receiver). The `*` is kept: it decides
  Go's method set and whether the method sees the caller's value or a copy,
  so it stays visible in the source (principle 3). The lowering is Go's
  receiver, named after the type: `func (c *Counter) Inc()`.
- The receiver is the keyword `self`. Because it is a keyword, nothing can
  declare it: a parameter, local, loop variable, or lambda parameter named
  `self` is a syntax error ("self is the method receiver; it cannot be
  declared or redeclared"), so `self` can never be shadowed. `self` itself
  cannot be assigned. Fields reached through it follow the usual rules: a
  pointer receiver's fields can be assigned; a value receiver's cannot,
  because the assignment would change only the copy (E0502, as before).
- Fields and methods also remain in scope by name inside a method
  (`Count += n`), as before. A local that hides a field draws warning W0006
  ("x hides the field x of self"), and `self.x` still reaches the field.
- There are no static methods. A type-level function is a package-level
  function, named as Go names constructors (`func NewServer(...)`), and is
  called as `NewServer(...)`. `Type.Name` in an expression is always a
  member of a value.
- Go's receiver syntax `func (s *Server) Run()` is an error with the edit
  `func *Server.Run()`.
- The design's example declares `func Server.ListProfiles(...)`: a value
  receiver, which is correct for a `Server` that holds a repository
  interface. A handler that changes the server is declared
  `func *Server.ListProfiles(...)`.

### Variables, assignment, and constants
```kotlin
name := "Ali"            // declares a mutable variable, type inferred
age := 30
age = 31                 // assigns
ratio: float64 = 2       // a type where inference is not enough
var buf: strings.Builder // (an error: inside functions, write buf: strings.Builder)
total: int               // the zero value
a, b := 1, 2
a, b = b, a              // Go's assignment lists, without parentheses
n, err := strconv.Atoi(s)
m, err := strconv.Atoi(t) // err is in this scope: := declares m and assigns err
var Default = "x"        // package level: var, as in Go
const MaxRetries = 5     // compile-time constant, Go's semantics
```
- `x := e` declares a new mutable variable in the current scope. `=`
  assigns to an existing one. Inside a function, `name: Type = e` and
  `name: Type` (the zero value) declare with a type; `val` and `var` are
  errors there, each with the edit that replaces it.
- A list `a, b := e` follows Go: names already declared in the same scope
  are assigned, the others are declared, and at least one must be new
  (E0005 otherwise). A name that is a parameter cannot be reused this way:
  parameters stay read-only. `x := e` when `x` is already declared in the
  same scope is E0005 with the hint "use = to assign".
- A declaration in an inner scope may hide an outer variable, as in Go; it
  draws warning W0006, which catches the shadowed `err` that
  `x, err := f()` in an inner block leaves unchanged in Go.
- Package-level variables are declared with `var`; top-level `val` is an
  error with the edit `var`. `const` declares compile-time constants with
  Go's semantics (untyped by default, `const X: int64 = 5` when typed);
  assigning to one is E0501. Constants are package-level only.
- Parameters, loop variables, `catch` bindings, and `self` stay read-only;
  value receivers keep their copy protection. Locals are mutable, as in Go.

**Smart casts.** A smart cast needs a value that cannot change between the
check and the use. Before, only `val`s qualified. Now parameters, `self`,
and any local the function never reassigns qualify: a local whose name is
never the target of `=`, a compound assignment, `++`/`--`, or `&`, and is
never reused by a later `:=` list, anywhere in the function body including
its lambdas. The analysis is syntactic and by name, so it is conservative:
reassigning one `x` unsettles every `x` in that function. A reassigned
local does not smart-cast; E0401 then says so and suggests a copy
(`now := cur; if now != null { ... }`). Package-level variables never
smart-cast, since any code in the package may assign them.

### Types
- Predeclared types use Go's names: `int`, `int8` ... `uint64`, `uintptr`,
  `float32`, `float64`, `complex64`, `complex128`, `byte`, `rune`, `bool`,
  `string`, `any`, `error`, and the constraint `comparable`. Lyth's own are
  `unit` (no result, in function types) and `nothing` (the type of `return`
  and `throw`, in diagnostics).
- `map[K]V`, `chan T`, `chan<- T`, `<-chan T`; `map` and `chan` are
  keywords. `*T`, `[]T`, `[N]T`, and function types `(A) -> R [throws]` are
  unchanged.
- A `?` after the element type of a slice, array, map, or channel belongs to
  the element (`map[string]*User?`, ADR 0023's rule). Maps and slices are
  never nullable (E0406); a nullable channel is `(chan T)?`. Elsewhere `?`
  applies to the whole type (`*User?`).
- Conversions use the type's name: `float64(n)`, `string(b)`, `[]byte(s)`,
  `int(color)`; `make(map[string]int)`, `make(chan int, 10)`.
- Diagnostics print types in this spelling, including platform types
  (`error!`) and untyped constants (`untyped int`).

### Generics
```kotlin
func Max[T: cmp.Ordered](items: []T): T { ... }
struct Page[T] { Items: []T }
func Page[T].First(): T = self.Items[0]
func []T.Map[T, R](transform: (T) -> R): []R { ... }

result := Max(numbers)               // inferred
other := Max[int](numbers)           // explicit
nf, ok := errors.AsType[*NotFound](err)
resp := APIResponse[[]Profile](Data = profiles)
```
- Type parameters follow the declared name in brackets. A generic type's
  methods name its parameters in the receiver and declare none of their
  own (Go has no generic methods); a generic extension function declares
  its parameters after its name, and its receiver may use them.
- Calls infer type arguments from the arguments, the expected type, and
  lambda results; explicit arguments are needed only when nothing
  determines them (`Zero[string]()`).
- **Indexing or instantiation.** The parser reads `x[A]` as an index
  expression whose operand may be a type: a type-only form (`[]T`,
  `map[K]V`, `chan T`) is parsed as a type, and `x[A, B]` can only be an
  instantiation. The checker decides `x[A]` as `go/types` does: when `x`
  names a generic function or type, Lyth or Go, it is an instantiation and
  the operand is read as a type (`*E`, `pkg.T`, `T?`, `Page[int]`);
  otherwise it is an index. The speculative `<T>` parse now runs only to
  report the old spelling `f<T>(x)` with its edit.
- **Constraint spelling: `T: C`, not Go's `T C`.** Go's `[T cmp.Ordered]`
  matches Go's parameters, `(x int)`. Lyth writes every name-and-type pair
  with a colon (parameters, fields, lambda parameters, typed locals), so
  `[T: cmp.Ordered]` is the consistent Lyth form, and dropping the colon in
  this one list would make Lyth inconsistent with itself to resemble Go in
  one place. The parser accepts Go's form only to report it with the edit
  that inserts the colon. `[T]` alone means `[T: any]`.

### Control flow
```kotlin
if n < 10 { "small" } else { "large" }
while count > 0 { count-- }
for user in users { ... }
for i, user in users { ... }
for key, value in counts { ... }
for i in 0..<n { ... }
when status { in 200..299 -> "ok"; else -> "error" }
when err := load() { ... }
select { v := ch.receive() -> ...; else -> ... }
```
- Conditions and subjects are not parenthesized and bodies are always
  blocks. A header expression cannot end in a trailing lambda (Go's rule
  for composite literals in headers); parenthesize it:
  `if (xs.Any { it > 0 }) { ... }`. A parenthesized condition is an
  ordinary parenthesized expression, so `if (c) {` still compiles. A body
  that is not a block is an error with the edit that adds braces.
- **`for ... in` stays; `range` is not adopted.** Lyth's `for x in xs`
  binds elements, `for i, x in xs` index and element, `for k, v in m` key
  and value, `for v in ch` received values, and `for i in a..<b` numbers.
  Go's `for x := range xs` binds the *index*: adopting `range` with Lyth's
  element-first meaning would make a false friend, and adopting it with
  Go's meaning would bring back `for _, x := range xs` for the common case
  and leave inclusive ranges (`1..n`) without a spelling. `in` is a
  different word for a different binding order, which is the principle's
  test. `for i, x := range xs` is an error whose hint gives the Lyth form
  (and, for `for _, x := range xs`, `for x in xs`).
- `for` only iterates; a condition loop is `while c`, an endless one
  `while true`. `for c {` would be ambiguous, since `x in xs` is also a
  membership test. Labels are unchanged (`outer@ for x in xs`).

### Errors, imports, concurrency
Unchanged in meaning; only the spellings above apply. A function that can
fail is `func f(): T throws` (Go `(T, error)`); `f()?` propagates; `catch`
handles; `throw` fails; Go's `(T, error)` results are handled the same way
or destructured with `:=`. Import paths stay quoted; Go's grouped
`import ( ... )` is not supported yet. Interface methods are written as in
Go, without a keyword: `interface Store { Find(id: int64): User? throws }`.
Concurrency keeps `go`, `defer`, `select`, and the channel methods
`send`/`receive`/`close` (ADR 0024).

### Deliberate differences from Go
| Lyth | Go | Why |
|---|---|---|
| `name: Type`, `func f(): R` | `name Type`, `func f() R` | one name-and-type rule, which nullable types (`User?`) and `throws` read well with |
| `throws`, `?`, `catch`, `throw` | `(T, error)`, `if err != nil` | the main improvement; lowers to Go's convention |
| `T?`, `?.`, `?:`, `!!`, smart casts | `*T`, nil checks | nil safety at compile time |
| `func T.M()`, `self` | `func (t T) M()` | the receiver is always the same name, and cannot be shadowed |
| `for x in xs` | `for _, x := range xs` | element first; ranges `a..<b`, `a..b` |
| `while c` | `for c` | `for` only iterates |
| `if`/`when` are expressions; `= expr` bodies | statements | fewer temporaries |
| `ch.send(v)`, `ch.receive()` | `ch <- v`, `<-ch` | ADR 0024 |
| `null` | `nil` | Lyth's null is never a typed nil (ADR 0026) |
| read-only parameters | assignable | ADR 0007, unchanged for parameters |
| `[T: C]` | `[T C]` | the name-and-type rule above |

### Rejected
Extending [ADR 0012](0012-rejected-features.md):

| Proposal | Why rejected | Instead |
|---|---|---|
| Untyped parameters, `add(a, b) { a + b }` | Types would come from call sites: the checker resolves signatures before bodies, exported APIs need stable signatures, and Go generics cannot express "any type with `+`" | typed parameters; inferred results only for unexported expression bodies (ADR 0021) |
| `=` declares a name that does not exist yet | with "current scope", `open = append(open, x)` in a loop body declares a new `open`; with "any scope", a typo declares a variable | `:=` declares, `=` assigns |
| `val`/`var` for locals | two keywords where Go has `:=`; immutability of locals was rarely what caught bugs, and smart casts now come from the reassignment analysis | `:=`, `name: Type = e` |
| `?` without `throws` | the signature would depend on the body; `return ErrBanned` would be ambiguous | `throws`, `throw` |
| Declarations without keywords (`add(...) { }`, `User { }`) | inside a body `add(a, b) { ... }` is a call with a trailing lambda; declarations stop being greppable | `func`, `struct` |
| Explicit receiver parameters (`User.greet(u)`, Go's `(u *User)`) | a different name in every method, which locals can shadow | `self` |
| Implicit return of a block's last expression | a trailing call (`w.Write(b)`) would silently become the result | `= expr`, `return` |
| `range` | a false friend or a regression (above) | `for ... in` |
| `for cond { }` | ambiguous with `for x in xs` | `while cond { }` |
| `spawn`, `channel<T>(n)` | renames of `go` and `make` | `go`, `make(chan T, n)` |
| Go's `<-` operator | kept rejected (ADR 0024) | channel methods; `<-` only in `<-chan T`, `chan<- T` |
| Unquoted import paths | `gopkg.in/yaml.v3` needs a separate lexer mode | `import "net/http"` |

## Alternatives
- **Keep the Kotlin spellings**: no migration, but two spelling systems and
  recovery code that turns Go habits into errors.
- **The inference-based language wholesale**: shorter helpers, but a
  different checker (bodies before signatures, a call-graph fixpoint for
  fallibility) and invisible boundaries; the saving disappears where Lyth
  code meets Go APIs, which need types anyway.
- **Keep `val` beside `:=`**: immutable locals, but two declaration forms
  for one concept; the smart-cast benefit is kept by the reassignment
  analysis.
- **Local `var x: T`** as Go writes zero-value declarations: rejected with
  `val` by the decision to have one declaration keyword; `x: T` covers it.
- **Go's constraint spelling `[T C]`**: see Generics.
- **`[K: V]` map types, matching the literal `["a": 1]`**: a third spelling
  of a type every Go tool prints as `map[K]V`.

## Consequences
- Go programmers write the types, generics, declarations, and headers they
  know; Lyth syntax marks what Lyth adds.
- The generated Go did not change: every `run/`, `gen/`, example, and
  benchmark program produces the same Go as before, apart from column
  numbers in panic messages, whose source moved.
- **Breaking changes.** Every earlier Lyth source must be rewritten. Each old
  spelling has a diagnostic naming its replacement, most with an exact
  edit: `fun` (E0008, edit `func`), local `val`/`var` (E0008, hint with the
  `:=` or `name: Type` form), top-level `val` (edit `var`), `Int` and the
  other old type names (E0101, edit), `Map<K, V>`/`Chan<T>`/`SendChan<T>`/
  `ReceiveChan<T>`/`List<T>` (E0008, edit), `<T>` type parameters and
  arguments (E0008), `[T C]` (edit), `this` (edit `self`), Go receivers
  (edit), `for (x in xs)` (edit), `for i, x := range xs`, brace-less bodies
  (edit), `(a, b) = (b, a)` (edit), `when (val x = e)` (edit), `select`'s
  `val v = ...`, and interface methods written with `func`.
- Locals lose compile-time immutability; parameters, loop variables, and
  value receivers keep it.
- `map`, `chan`, and `self` are keywords; `fun` and `this` no longer are.
- Package-level variables no longer smart-cast; copy one into a local.
- `const` is still package-level only; Go also allows local constants.

### Implementation
The repository was migrated by a one-off tool built on the previous parser,
which rewrote sources at AST spans (`tests/testdata`, `std/`, `examples/`,
`bench/`, Lyth blocks in the docs, and Lyth sources inside Go tests), and
which was then deleted. Tests: `run/declarations`, `run/methods`,
`run/typeargs`, `gen/syntax`, `check/declarations_neg`, `check/self_neg`,
`check/syntax_neg` (one diagnostic per old spelling), and the migrated
suites.
