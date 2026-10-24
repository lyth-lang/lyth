# Lyth Language Specification (v0.1 draft)

This is the normative description of Lyth's syntax and the semantics that are not
about types. Types, inference, nullability, and mutability are specified in
[types.md](types.md); error handling in [errors.md](errors.md). Each section is
tagged **[v0.1]** (implemented in the current compiler, with tests) or
**[planned]** (designed, not yet implemented).

Notation: EBNF as in the Go specification. `|` alternation, `()` grouping,
`[]` option, `{}` repetition. Terminals are in double quotes.

## 1. Source files and lexical structure

### 1.1 Files **[v0.1]**

A source file is UTF-8 text with the extension `.lyth`. A byte order mark is
rejected. All files in one directory belong to one package (as in Go).

File names follow Go's build-constraint conventions so generated files keep
them: `foo_linux.lyth` compiles only for Linux, and `foo_test.lyth` is a test file.
See [compiler.md §6.2](../design/compiler.md#62-generated-file-names).

### 1.2 Comments **[v0.1]**

`// line comment` and `/* block comment */` (not nested). A run of line comments,
each on a line of its own, immediately preceding a declaration, struct field, or
interface method is its doc comment and is copied to the generated Go declaration
so `go doc` works on Lyth packages. A comment after code on the same line is not a
doc comment.

### 1.3 Newlines and statement termination **[v0.1]**

Lyth is newline-sensitive. The lexer emits a `NEWLINE` token at each
line break outside of `( )` and `[ ]`. A `NEWLINE` terminates a statement or
declaration except when:

1. the line ends with a token that cannot end an expression: a binary operator,
   `.`, `,`, `(`, `[`, `{`, `=`, `:=`, `->`, `:`, or a prefix operator; or
2. the next line begins with `.`, `?.`, `?:`, `&&`, `||`, `catch`, `else`, or
   `)`/`]`/`}` closing an open bracket.

Semicolons may separate statements on one line. They are never required.

> Rationale: rule 2 lets fluent call chains and elvis chains start lines with an
> operator, a common layout for long chains. A trailing lambda's `{` must therefore be on
> the same line as the call it belongs to; `{` at the start of a line begins a
> new statement. In the header of an `if`, `while`, `for`, or `when`, a `{`
> always starts the body (§4.4).

### 1.4 Identifiers **[v0.1]**

```ebnf
identifier = letter { letter | digit } .
letter     = unicode_letter | "_" .
```

`_` alone is the blank identifier. Identifiers may name Go entities verbatim,
including Go keywords that are not Lyth keywords (`type`, `range`, `default`,
`switch`, `case`, `goto`, `fallthrough`). The
code generator renames them when they would clash in Go (see §8.3). A Go
identifier that is a Lyth keyword is referenced with backticks: `` obj.`is` ``
**[planned]**.

### 1.5 Keywords **[v0.1]**

Hard keywords (cannot be identifiers):

```
as break catch chan const continue defer else enum false for func go if
import in interface is map null package return sealed select self struct
throw throws true typealias var when while
```

`var` declares package-level variables only (§3.5). `self` is the receiver of
a method or extension function (§3.2) and can never be declared. `val` is
reserved only so that code in the old syntax gets a precise error (E0008, with
the `:=` or `var` form that replaces it). `pub` and `private` are reserved for
the same reason: Lyth has no visibility keywords (§3.1). `fun` and `this` are
not keywords; a declaration beginning with `fun` is E0008 with the edit
`func`, and `this` is E0008 with the hint `self`.

### 1.6 Operators and punctuation **[v0.1]**

```
+  -  *  /  %  &  |  ^  <<  >>  &&  ||  !  ==  !=  <  <=  >  >=
=  +=  -=  *=  /=  %=  &=  |=  ^=  <<=  >>=  ++  --
:=  .  ?.  ?  ?:  !!  ..  ..<  ->  ...  ,  ;  :  @
(  )  [  ]  {  }
```

`:=` declares local variables (§4.1). Go's `<-` operator does not exist: `<-`
appears only in the channel types `<-chan T` and `chan<- T`, and `<-ch` or
`ch <- v` is E0008 with the method spelling (§5.9).

### 1.7 Literals **[v0.1]**

```ebnf
int_lit    = decimal | "0x" hex_digits | "0o" octal_digits | "0b" binary_digits .
float_lit  = decimal "." decimal [exponent] | decimal exponent .
rune_lit   = "'" ( unicode_char | escape ) "'" .
string_lit = '"' { string_char | escape | template } '"' .
template   = "${" Expression "}" .
raw_string = "`" { any char except "`" } "`" .
```

- Digits may be separated by `_` (`1_000_000`). A number starts with a digit
  (no `.5`).
- Escapes: `\n \r \t \\ \" \' \$ \0 \uXXXX \U00XXXXXX \xHH`.
- **String templates.** `${expr}` inside a `"..."` literal interpolates. There is
  exactly one template syntax. `$` followed by an identifier character without
  braces is a compile error (E0006, "did you mean `${name}`?"). This catches the
  habit `"$name"` from other languages, which would otherwise print literally. A `$` followed by
  anything else (`"$1"`, `"US$ 5"`) is a literal dollar, so SQL placeholders need
  no escaping. Write `\$` for a literal `$` before an identifier character.
- **Raw strings** use backticks, exactly as in Go: no escapes, no templates, may
  span lines. They are also the syntax for struct tags.
- Integer and float literals are *untyped constants* (Go semantics).
- `true`, `false`, and `null` are keywords.

## 2. Packages and imports

```ebnf
SourceFile    = [ PackageClause ] { ImportDecl } { TopLevelDecl } .
PackageClause = "package" identifier NEWLINE .
ImportDecl    = "import" string_lit [ "as" ( identifier | "_" ) ] NEWLINE .
```

**[v0.1]** A package is a directory. The package clause is optional. When it is
omitted, the name is `main` if a file in the package declares a top-level
`func main()`, otherwise the directory name (which must then be a valid
identifier). External test packages (`package foo_test`) must be explicit.

**[v0.1]** Import paths are Go import paths, resolved by the Go toolchain
against the enclosing module's `go.mod`. An import may refer to a Go package or a
Lyth package; the importer cannot tell the difference and does not need to. The
default qualifier is the imported package's declared name (e.g. `pgx` for
`github.com/jackc/pgx/v5`), not the last path element. `as name` renames the
qualifier; `as _` imports for side effects (database drivers). Dot-imports do not
exist.

Unused imports are a warning (W0002); the generated Go simply omits them.

## 3. Declarations

```ebnf
TopLevelDecl = { Annotation } ( FuncDecl | StructDecl | InterfaceDecl | SealedDecl
             | EnumDecl | VarDecl | ConstDecl | TypeAliasDecl ) .
```

### 3.1 Visibility and Go names **[v0.1]**

Visibility follows Go's rule. A top-level declaration, method, struct field, or
interface method whose name starts with an uppercase letter is **exported**
(visible to other packages); any other name is **unexported**. There are no
visibility keywords: `pub` and `private` are E0008, with a hint giving the
renamed declaration.

The Go name of a Lyth declaration is its Lyth name, so the name in Lyth source is
the name in the generated code, in stack traces, in `go doc`, and to Go
callers. The one exception is an unexported name that is a Go keyword or
predeclared identifier, which gets a trailing `_` (`func string()` is
`string_` in Go, `len` is `len_`). Two declarations whose Go names collide are
an error (E0102).

Go identifiers are used verbatim as well (`http.ListenAndServe`, `r.URL`), so
one naming convention covers Go and Lyth APIs. Initialisms are written by the
programmer, as in Go: `ID`, `UserID`, `ServeHTTP`
([ADR 0022](../adr/0022-capitalization-is-visibility.md)).

### 3.2 Functions **[v0.1]**

```ebnf
FuncDecl    = "func" [ Receiver "." ] identifier [ TypeParams ]
              "(" [ ParamList ] ")" [ ":" ResultType ] [ "throws" ] FuncBody .
TypeParams  = "[" TypeParam { "," TypeParam } "]" .
TypeParam   = identifier [ ":" Type ] .
Receiver    = [ "*" ] Type .
ParamList   = Param { "," Param } [ "," ] .
Param       = identifier ":" [ "..." ] Type [ "=" Expression ] .
ResultType  = Type | "(" Type "," Type { "," Type } ")" .
FuncBody    = Block | "=" Expression .
```

- `func name(...)` with a block body and no result type returns nothing. A
  block body returns its result with `return`; there is no implicit return of
  the last expression. An
  unexported function with an expression body may leave the result type out: it
  is the type of the expression (`func square(n: int) = n * n`), or nothing for
  an expression without a value (`func log(x: int) = println(x)`). Inference
  follows references, so declaration order does not matter, but a result that
  depends on itself must be declared (E0208). An exported function always declares
  its result type, because it is part of the package's API, and so does a
  fallible body: `func port(s: string): int throws = s.toInt()` (E0208).
  [ADR 0021](../adr/0021-inferred-results-and-pass-through.md) has the
  rationale.
- Parameters are read-only: assigning one is E0501, taking its address is
  E0503, and a `:=` list cannot reuse one (E0501). Copy a parameter into a
  local to change it. `xs: ...int` declares a variadic
  parameter whose type inside the body is `[]int`; callers spread a slice
  with `xs...` (Go syntax).
- `throws` makes the function fallible: its Go signature gets a trailing
  `error` result. Semantics in [errors.md](errors.md).
- A parenthesized result list declares multiple results (a *tuple*), returned
  as in Go: `return a, b`. Tuples
  exist only as results and are consumed by destructuring (§4.2).
- `func main() throws` is allowed. The generated `main` prints the error to
  stderr and exits with status 1.
- A parameter may have a **default value**:
  `func connect(host: string, port: int = 5432, timeout: time.Duration = 30 * time.Second)`.
  A call that leaves the parameter out gets the default expression at the call
  site, evaluated after the explicit arguments, on every call. The expression
  is resolved where the function is declared, so a caller's locals never
  change its meaning. It must be a plain expression, without control flow or
  error and null operators (`?`, `catch`, `if`, `when`, `?:`, `!!`, `is`,
  `in`, `as`), and cannot refer to other parameters (E0216). In an exported
  function it may use only exported names (E0105), since calls in other packages
  contain it. Variadic parameters and interface methods cannot have defaults.
  The Go signature is unchanged: Go callers pass every argument. Calls name
  arguments as described in §5.3 ([ADR 0019](../adr/0019-named-and-default-arguments.md)).

**Methods [v0.1].** `func User.FullName(): string` declares a method with a
value receiver; `func *User.Rename(name: string)` a method with a pointer
receiver. The `*` decides Go's method set and whether the method sees the
caller's value or a copy, so it stays visible. Both lower to a Go receiver
named after the type (`func (u *User) Rename(name string)`). The receiver type
must be declared in the same package (Go rule). Inside the body, the keyword
`self` is the receiver, and members are also in scope by bare name (`Name`
means `self.Name` unless a local or parameter hides it). A local that hides a
field draws warning W0006 ("x hides the field x of self"); `self.x` still
reaches the field.

- `self` cannot be assigned (E0501). Because it is a keyword, nothing can
  declare it: a parameter, local, loop variable, or lambda parameter named
  `self` is a syntax error (E0003), so `self` is never shadowed.
- A value receiver is a copy, so assigning to its fields is E0502, which
  catches Go's silent "lost write through value receiver" bug. A pointer
  receiver's fields can be assigned.
- There are no static methods. A type-level function is a package-level
  function, named as Go names constructors (`func NewServer(...)`).
- Go's receiver syntax, `func (s *Server) Run()`, is E0008 with the edit
  `func *Server.Run()`.

A generic struct's methods name its type parameters in the receiver,
`func Page[T].First(): T` (Go's `func (p Page[T]) First() T`). A method cannot
declare type parameters of its own, because Go has no generic methods; a
generic function or extension function takes that role.

**Extension functions [v0.1].** The method syntax with any other receiver type
declares an extension function: a type of another package, a type without
methods of its own, or an interface (`func string.Shout(): string`,
`func []T.Second[T](): T`, `func time.Duration.Halved()`,
`func Shape.Describe()`). It is called like a method, `"hi".Shout()`, and
lowers to an ordinary Go function that takes the receiver as its first
parameter, `Shout("hi")`. The rules:

- The body reaches the receiver through `self`; there is no implicit member
  scope as in methods.
- A member always wins over an extension of the same name. An extension is
  in scope in its own package and, when exported, in every file that imports
  its package; two applicable extensions from different packages are
  ambiguous (E0102).
- A pointer receiver applies to an addressable value through its address and
  a value receiver to a pointer; an interface receiver applies to the types
  that implement it. An untyped constant matches by its default type.
- An extension declares its type parameters after its name, and its receiver
  may use them. They are inferred from the receiver first, then from the
  arguments.
- Go has one function per name, so a package declares at most one extension
  of a name, and the name cannot be used by another top-level declaration.
- An extension must be called; it has no method value. Nullable receivers,
  bare type parameters as receivers, and `?.` calls are not supported
  (E0601, E0216).
- Extensions never satisfy interfaces.

See [ADR 0029](../adr/0029-extension-functions.md).

**Generic functions [v0.1].** `func name[T: Constraint](...)` declares type
parameters, which map 1:1 to Go type parameters; `[T]` alone means
`[T: any]`. The constraint follows a colon, as every Lyth name-and-type pair
does; Go's `[T cmp.Ordered]` is E0008 with the edit that inserts it. Type
arguments are inferred at calls or given as `name[int](...)`. The rules are in
[types.md §10](types.md#10-generics).

### 3.3 Structs **[v0.1]**

```ebnf
StructDecl = "struct" identifier [ TypeParams ] [ ":" Type { "," Type } ] "{" { FieldDecl } "}" .
FieldDecl  = identifier ":" Type [ raw_string ] NEWLINE .
```

```kotlin
struct User {
    ID: int64
    Name: string
    Email: string?               `json:"email,omitempty"`
    passwordHash: string
}
```

- Field visibility follows §3.1: `Name` is exported, `passwordHash` is not.
  Reflection-based Go libraries (`encoding/json`, database scanners) see only
  exported fields.
- A raw-string tag is emitted verbatim. If the tag has no `json` key, Lyth
  prepends one for exported fields, in lower camel case: the leading uppercase
  run is lowered, keeping the last letter of an initialism that starts the next
  word (`Name` → `name`, `ID` → `id`, `UserID` → `userID`, `HTTPPort` →
  `httpPort`). Unexported fields get no tags
  ([ADR 0013](../adr/0013-struct-fields-and-json.md),
  [ADR 0022](../adr/0022-capitalization-is-visibility.md)).
- Struct embedding in Lyth-declared structs is **[planned]**. Embedded fields of
  Go structs work, because promotion is resolved by `go/types`.

**Construction [v0.1].** A struct type is constructed by calling it:

```kotlin
u := User(ID = 1, Name = "Ada")          // named arguments, any order
p := Point(1, 2)                          // positional: all fields, declaration order
srv := &http.Server(Addr = ":8080")       // Go structs: named arguments only
```

A generic struct (`struct Page[T] { Items: []T }`) is constructed with its type
arguments, `Page[User](Items = users)`, or without them when the fields
determine them, `Page(Items = users)`; the generated composite literal always
names them (`Page[User]{...}`), as Go requires.

Named and positional arguments cannot be mixed. Omitted fields take their zero
value, which is an error for fields whose type has no zero value in Lyth (e.g. a
non-nullable pointer, see [types.md §6](types.md#6-zero-values-v01)). Construction always
generates a keyed Go composite literal, so it is robust to field reordering and
passes `go vet`'s composites check. `&T(...)` takes the address of a fresh value.

**Declared conformance [v0.1].** `struct Circle : Shape, fmt.Stringer { ... }`
declares that `Circle` implements the listed interfaces. Interfaces stay
structural: the declaration adds no behavior and is never required. It is a
compile-time check, made once all methods of the package are known: `Circle` or
`*Circle` must implement each interface. Otherwise E0212 at the declaration
names the method, the signature the interface needs, and the reason (missing,
a different signature, a name that differs only in case), instead of a type mismatch at some distant
use. Each entry lowers to Go's assertion idiom, `var _ Shape = Circle{}`, or
`var _ io.Writer = (*counter)(nil)` when only the pointer type implements the
interface (pointer-receiver methods). Listing a type that is not an interface
is E0201: Lyth has no inheritance
([ADR 0020](../adr/0020-declared-conformance.md)).

### 3.4 Interfaces **[v0.1]**

```ebnf
InterfaceDecl = "interface" identifier [ TypeParams ] "{" { MethodSpec | Type NEWLINE } "}" .
MethodSpec    = identifier "(" [ ParamList ] ")" [ ":" ResultType ] [ "throws" ] NEWLINE .
```

Interfaces are structural, exactly as in Go. A method is written as in Go,
without a keyword (`interface Shape { Area(): float64 }`); writing `func`
before it is E0008. A type implements an interface by
declaring methods with the same names and signatures; an exported interface
method (`Area(): float64`) needs an exported method, an unexported one
(`ping()`) can only be implemented in the same package.
A bare `Type` line embeds another interface. A struct may declare the
interfaces it implements (§3.3). An interface may be generic,
`interface Container[T] { Get(): T }`. Declaring type-set constraints
(`~int | ~float64`) is **[planned]**; Go's (`cmp.Ordered`) work today.

### 3.5 Variables and constants **[v0.1]**

```ebnf
VarDecl   = "var" identifier [ ":" Type ] "=" Expression NEWLINE .
ConstDecl = "const" identifier [ ":" Type ] "=" Expression NEWLINE .
```

A top-level `var` declares a Go package-level variable, with Go's dependency-
ordered initialization. It is mutable: any code in the package (and, when it
is exported, any importing package) may assign it, so it never smart-casts
([types.md §5.3](types.md#53-using-nullable-values-v01)). A top-level `val` is
E0008 with the edit `var`. `const` declares a Go constant: the initializer must
be a constant expression, untyped unless a type is given
(`const X: int64 = 5`); assigning one is E0501. Both are package-level only:
inside a function, local variables are declared with `:=` (§4.1), and `const`
is E0601.

### 3.6 Type aliases **[v0.1]**

```ebnf
TypeAliasDecl = "typealias" identifier "=" Type NEWLINE .
```

A type alias gives an existing type a second name; it does not create a new
type, so values convert freely and the alias has the methods of the aliased
type. It lowers to a Go alias declaration (`typealias Handler = (Ctx) -> unit
throws` becomes `type Handler = func(Ctx) error`). Aliases are how Lyth names
function types and long instantiations without the cost of a new type. A Go
defined type (`type Celsius float64`) is declared with `struct`/`interface` or
is **[planned]** for other underlying types. An alias may refer to other
aliases but not to itself (E0216). Generic aliases are **[planned]**.

### 3.7 Enums and sealed interfaces **[v0.1]**

```ebnf
EnumDecl   = "enum" identifier "{" identifier { ( "," | NEWLINE ) identifier } [ "," ] "}" .
SealedDecl = "sealed" InterfaceDecl .
```

**Enums.** `enum Color { Red, Green, Blue }` declares a named integer type
with one constant per entry, lowered to Go's `iota` idiom and a generated
`String()` method that returns the entry's name (`Color(7)` for a value that
is not an entry). Entries are separated by commas or newlines; they carry no
values or bodies (E0601), since data belongs in a sealed interface's members.

- Lyth code names an entry through its type, `Color.Red`. The Go constant is
  the type's name followed by the entry name, `ColorRed`, so entries of
  different enums never collide in Go's package scope. This is the one place
  where a Go name differs from the Lyth spelling (§3.1).
- Visibility follows the type's name. The zero value is the first entry.
- Enums support `==` and `!=`, not ordering (E0209). `int(c)` gives the
  value and `Color(n)` converts back, as in Go. `encoding/json` encodes an
  enum as its number.
- An enum may have methods (`func Color.hex(): string`) but not one named
  `String`, which is generated (E0102).

**Sealed interfaces.** `sealed interface Shape { ... }` is an interface that
only types declared in the same package can implement, by declaring it
(`struct Circle : Shape`, §3.3). Its members are those structs: `T`, or `*T`
when only the pointer type has the methods. Another package cannot add a
member (E0212), and a struct that has the methods but does not declare the
interface does not implement it (E0201 says why). The Go interface gets an
unexported marker method (`isShape()`), and each member a no-op
implementation of it.

**Exhaustive `when`.** A `when` whose subject is a non-null enum or sealed
interface must cover every entry or member type, or have an `else`
(E0216 names what is missing): adding an entry or member is a compile error at
every `when` that does not handle it. An exhaustive `when` needs no `else`,
as a value or as a statement that terminates. Inside such a `when`, entries
may be written unqualified (`Red -> ...`), also when the subject is a
nullable enum. The generated `switch` gets a `default` that panics with the
Lyth position, because Go cannot see that it is exhaustive and a converted
integer or a nil interface must not pass silently
([ADR 0025](../adr/0025-enums-and-sealed-interfaces.md)).

```kotlin
enum Level { Debug, Info, Warn }

sealed interface Event {
    Level(): Level
}

struct Login : Event {
    User: string
}

func Login.Level(): Level = Level.Info

struct Failure : Event {
    Reason: string
}

func Failure.Level(): Level = Level.Warn

func describe(e: Event): string = when e {
    is Login -> "login by ${e.User}"
    is Failure -> "failure: ${e.Reason}"
}

func main() {
    events: []Event = [Login(User = "ada"), Failure(Reason = "disk full")]
    for e in events {
        tag := when e.Level() {
            Debug -> "D"
            Info -> "I"
            Warn -> "W"
        }
        println(tag, describe(e))
    }
}
```

## 4. Statements

```ebnf
Block      = "{" { Statement } "}" .
Statement  = ShortVarDecl | TypedVarDecl | Assignment | IncDec | Return | Throw
           | Break | Continue | Defer | Go | For | While | Select | Expression .
```

### 4.1 Local declarations **[v0.1]**

```ebnf
ShortVarDecl = BindingName { "," BindingName } ":=" ExprList .
TypedVarDecl = identifier ":" Type [ "=" Expression ] .
BindingName  = identifier | "_" .
ExprList     = Expression { "," Expression } .
```

- `x := e` declares a new variable in the current scope, with the type of `e`
  (an untyped constant takes its default type, [types.md §9](types.md#9-type-inference-v01)).
  `x: T = e` declares it with a type, and `x: T` with the zero value of `T`,
  which must have one (E0405). The zero-value form covers Go idioms like
  `wg: sync.WaitGroup` and `buf: bytes.Buffer`.
- Local variables are mutable, as in Go: `=` assigns to one (§4.3).
  Parameters, loop variables, `catch` bindings, and `self` are the read-only
  names of a function body ([types.md §7](types.md#7-mutability)).
- `x := e` when `x` is already declared in the same scope, including a
  parameter in the function's outermost block, is E0005 with the hint "use =
  to assign". Redeclaring a name with `x: T` in the same scope is E0102.
- Inside a function, `val` and `var` are E0008, with a hint giving the `:=`
  or `name: Type` form; `const` is E0601 (constants are package-level, §3.5).
- A declaration in an inner block may hide a variable or parameter of an
  enclosing scope, as in Go. It draws warning W0006, which catches the
  shadowed `err` that `x, err := f()` in an inner block leaves unchanged in
  Go. In a method, a local that hides a field of `self` draws W0006 as well
  (§3.2).
- An unused local is warning W0001. Go would reject the generated code, so the
  generator emits `_ = x` to keep it valid.

### 4.2 Destructuring **[v0.1]**

`a, b := f()` binds the results of a multi-result call, and
`lo, hi := 0, len(xs)` binds several values at once. The number of names
must match; `_` discards a result. As in Go, names already declared in the
same scope are assigned and the others are declared, and at least one must be
new (E0005, "no new variables on the left of :="): after `n, err := f()`,
`m, err := g()` declares `m` and assigns `err`. A parameter cannot be reused
this way (E0501). Destructuring is the raw, Go-shaped way
to consume results and the way to access `(value, error)` pairs whose value is
meaningful alongside the error (`io.Reader.Read`). See
[errors.md §5](errors.md#5-raw-access-destructuring).

### 4.3 Assignment **[v0.1]**

```ebnf
Assignment = Target { "," Target } "=" ExprList
           | Expression assign_op Expression .
Target     = Expression | "_" .
```

`lhs = rhs` and compound forms `+= -= *= /= %= &= |= ^= <<= >>=`. The left side
must be *assignable*: a local variable, a package-level `var`, a field or
element reached through a mutable path, or `*p` (see
[types.md §7](types.md#7-mutability)). Assigning a parameter, loop variable,
`catch` binding, `self`, or constant is E0501. Assignment is a statement, not
an expression. `x++`/`x--` are statements.

An **assignment list** assigns several targets at once: `a, b = b, a + b`.
All right-hand values are evaluated before any target is assigned, so this
swaps or steps without a temporary, exactly like Go's `a, b = b, a+b`, to which
it lowers. Targets are assignable expressions (`xs[i], xs[j] = xs[j], xs[i]`)
or `_`. The right side has one value per target or is a multi-result call
(`n, err = strconv.Atoi(s)`). Compound operators take a single target
(E0216). The list has no parentheses: `(a, b) = (b, a)` is E0008 with the
edit that removes them, and a parenthesized list is not a value anywhere
(E0216).

### 4.4 Control flow **[v0.1]**

```ebnf
If       = "if" Expression Block [ "else" ( If | Block ) ] .
While    = [ Label ] "while" Expression Block .
For      = [ Label ] "for" BindingName [ "," BindingName ] "in" Expression Block .
Label    = identifier "@" .
Return   = "return" [ Expression { "," Expression } ] .
Throw    = "throw" Expression .
Break    = "break" [ "@" identifier ] .
Continue = "continue" [ "@" identifier ] .
```

- Conditions and subjects are not parenthesized, and bodies are always
  blocks: `if c { ... } else if d { ... } else { ... }`. A body that is not
  a block is E0008 with the edit that adds the braces. A parenthesized
  condition is an ordinary parenthesized expression, so `if (c) {` also
  compiles.
- A header expression cannot end in a trailing lambda, because the `{` after
  it starts the body (Go's rule for composite literals in headers).
  Parenthesize it: `if (xs.Any { it > 0 }) { ... }`.
- `while c { ... }` loops while `c` holds; an endless loop is
  `while true { ... }`. `for` only iterates: `for c { ... }` is a syntax
  error, since `x in xs` is also a membership test.
- `for x in e` iterates like Go's `range`, but binds the element first:
  - `[]T`/`[N]T`: `x` is the element; `for i, x in xs` gives index and element.
  - `map[K]V`: `for k, v in m`; `for k in m` gives keys. Iteration order is
    Go's, i.e. unspecified.
  - `string`: runes; `for i, r in s` gives byte offset and rune.
  - `chan T`: values until the channel is closed.
  - ranges `a..b` (inclusive) and `a..<b` (exclusive) of integer type.
  - Go 1.23 iterator functions (`iter.Seq[T]`, `iter.Seq2[K, V]`) **[planned]**.
  Go's `for i, x := range xs` is E0008; its hint gives the Lyth form (for
  `for _, x := range xs`, `for x in xs`). Loop variables are read-only and
  fresh per iteration (Go 1.22 semantics), so closures capture them safely.
- `break` and `continue` apply to the innermost loop, also inside `when`. (Go's
  `break` inside a `switch` exits the switch; the generated code labels the
  loop where needed.)
- A loop labeled `outer@ for x in xs` or `outer@ while c` is the target of
  `break@outer` and `continue@outer` anywhere in its body, including nested
  loops. The `@` touches the name. Labels name loops only (E0008).
  A jump to a label that no enclosing loop of the same function or lambda has
  is E0101, and reusing an enclosing loop's label is E0102. Labels lower to Go
  labels, emitted only where a jump needs one.
- `defer call` and `go call` behave as in Go. Both also accept a block:
  `go { ... }` is `go func() { ... }()`. A deferred call that returns an error
  must be handled with `catch` (`defer f.Close() catch {}`), or with `?` once
  that is implemented ([errors.md §7](errors.md#7-defer-panic-and-recover)). A
  `go` call cannot return an unhandled error.

### 4.5 Expression statements **[v0.1]**

Only calls (including the channel operations `ch.send(v)` and
`ch.receive()`) and postfix `?`/`catch` forms may stand
alone as statements. A call whose result includes an `error` cannot be a bare
statement (E0303); see [errors.md §4](errors.md#44-no-silently-discarded-errors).
Ignoring a non-error result is allowed, as in Go.

## 5. Expressions

### 5.1 Precedence

From lowest to highest. All binary operators are left-associative except `?:`,
which is right-associative.

| Level | Operators | Notes |
|---|---|---|
| 1 | `\|\|` | |
| 2 | `&&` | |
| 3 | `==` `!=` | Go puts these at one level with `<`; Lyth separates them |
| 4 | `<` `<=` `>` `>=` | |
| 5 | `in` `!in` `is` `!is` | membership (§5.11) and type tests (§5.10) |
| 6 | `?:` | elvis; right side may be `return`/`throw`/`break`/`continue` |
| 7 | `..` `..<` | ranges, only in `for` and `in` |
| 8 | `+` `-` `\|` `^` | Go's additive level |
| 9 | `*` `/` `%` `<<` `>>` `&` | Go's multiplicative level |
| 10 | `as` `as?` | |
| 11 | prefix `-` `+` `!` `^` `&` `*` | |
| 12 | postfix `.` `?.` `?` `!!` call `[...]` `catch` | |

Arithmetic and bitwise levels match Go, so generated expressions need no extra
parentheses and Go readers' intuitions hold. Comparisons are split
so that `a < b == c < d` means `(a < b) == (c < d)`.

### 5.2 Primary expressions **[v0.1]**

Identifiers, literals, `self`, parenthesized expressions, list literals
`[a, b]`, map literals `[k: v]` (empty map `[:]`), lambdas, `if`, and `when`.

Collection literals take their type from the expected type, or else from their
elements. `[]` and `[:]` require an expected type. Where an array is expected,
a list literal is an array literal and gives every element:
`a: [3]int = [1, 2, 3]` lowers to `[3]int{1, 2, 3}` (a different count is
E0201). The expected type comes from a declaration, assignment, argument,
return, field, or enclosing literal, never from the other operand of an
operator: `arr == [1, 2, 3]` compares with a slice (E0202, whose fix is
`arr == [3]int{1, 2, 3}`). Where a Go named slice, array, or map type is
expected (`http.Header`, `url.Values`, `gin.H`), the literal has that type and
its elements are typed by the type's elements: `h: http.Header = ["Accept":
["text/plain"]]` lowers to `http.Header{"Accept": {"text/plain"}}`.

Typed literals spell the type, as in Go: `[]int64{1, 2}`,
`map[string]any{"status": "ok", "count": 3}`, `[3]int{1, 2}` (the rest is
zero), and `[...]string{"a", "b"}`. Elements are expressions typed by the
element type (`[][]int{[1, 2]}`); Go's elided `{1, 2}` and indexed `{0: a}`
elements are E0008, and in a header the literal must be parenthesized.
Go's struct literal `Point{X: 1}` is E0008 with the construction call
`Point(X = 1)`, and Go's literal of a named map type `gin.H{"k": v}` is E0008
with the conversion `gin.H(["k": v])` (ADR 0032). A constant map key given twice is E0102. A list literal lowers to a Go
slice literal `[]T{...}`, which is non-nil even when empty, so it serializes as
`[]`, not `null`.

### 5.3 Calls, construction, conversion **[v0.1]**

`f(args)` is a function call, a struct construction (§3.3), or a conversion when
`f` names a non-struct type (`int64(x)`, `float64(n)`, `time.Duration(n)`,
`string(bytes)`, `[]byte(s)`).
Conversions follow Go's rules exactly; a lambda converted to a named function
type takes its parameter types from that type
(`http.HandlerFunc({ w, r -> ... })`), and a collection literal converted to a
type is a literal of that type: `gin.H(["error": msg, "code": 7])` lowers to
`gin.H{"error": msg, "code": 7}`. A trailing lambda may follow the
argument list on the same line: `app.get("/health") { c -> c.text(200, "ok") }`.
Explicit type arguments follow the name in brackets, `Max[int](xs)` or
`errors.AsType[*NotFound](err)`: `x[A]` is an instantiation when `x` names a
generic function or type, and an index otherwise
([types.md §10](types.md#10-generics)).

**Named arguments [v0.1].** Calls to functions and methods declared in Lyth may
name arguments: `connect("db", timeout = time.Second)`. Positional arguments
come first and fill parameters in order; named ones follow in any order, and
parameters left out take their defaults (§3.2). Naming a parameter twice is
E0102; an unknown name or a missing argument is E0203. A trailing lambda fills
the last parameter, and a variadic parameter is passed positionally only.
Arguments are evaluated in the order written. The call lowers to an ordinary
positional Go call with the defaults filled in, and arguments with side effects
move into temporaries only when that order would otherwise change. Go
functions take positional arguments only (E0216), because Go parameter names
are not part of an API and change between versions.

### 5.4 Member access **[v0.1]**

`x.name` selects a field or method. Pointers are dereferenced automatically and
addresses are taken automatically for pointer-receiver calls, as in Go, subject
to mutability ([types.md §7](types.md#7-mutability)).

`x?.name` is the **safe call**: when `x` is null the result is null, otherwise
the member is accessed. When `x` is a *fallible result* (a call returning an
error), `x?.name` means `(x?).name`: propagate the error, then access the
member. See [errors.md §3.3](errors.md#33-chaining).

### 5.5 Null-handling operators **[v0.1]**

`a ?: b` evaluates to `a` if it is non-null, otherwise to `b` (evaluated only
when needed). `b` may be a diverging expression: `?: return x`, `?: throw e`,
`?: continue`. `x!!` asserts that `x` is non-null; if it is null, the program
panics with a message naming the source position. Details are in
[types.md §5](types.md#5-nullability).

### 5.6 Error operators **[v0.1]**

Postfix `e?` propagates an error. `e catch (err) { ... }` handles it locally. See
[errors.md](errors.md).

### 5.7 `if` and `when` expressions **[v0.1]**

`if c { a } else { b }` is an expression when used as a value
(`label := if n < 10 { "small" } else { "large" }`). The `else` branch is
then required. Each branch is a block and yields the value of its last
expression.

```ebnf
When       = "when" [ [ identifier ":=" ] Expression ] "{" { WhenBranch } "}" .
WhenBranch = WhenCond { "," WhenCond } "->" Body NEWLINE | "else" "->" Body NEWLINE .
WhenCond   = Expression | ( "is" | "!is" ) Type | ( "in" | "!in" ) Expression .
```

With a subject, conditions are compared with `==`, type-tested with `is T` /
`!is T` (§5.10), or membership-tested with `in c` / `!in c` (§5.11). Without a subject, each condition is a `bool`. Branches are
tested in order, each condition is evaluated only if no earlier one matched, and
there is no fallthrough. A `when` used as a value must have an `else` branch,
unless it is exhaustive: a `when` over an enum or a sealed interface must
cover every entry or member type or have an `else`
([§3.7](#37-enums-and-sealed-interfaces-v01)).

A `when` whose conditions are all `is` tests lowers to a Go type switch;
`when r := f() { ... }` binds the subject for the branches. Other `when`s lower to a Go `switch`, or
to an `if`/`else` chain when a condition needs statements (a `?`, a mix of `is`
and value conditions). When an `if` or `when` value is returned, each branch
returns directly.

### 5.8 Lambdas **[v0.1]**

```ebnf
Lambda      = "{" [ LambdaParams "->" ] { Statement } "}" .
LambdaParams = LambdaParam { "," LambdaParam } .
LambdaParam = identifier [ ":" Type ] .
```

- Parameter types come from the expected function type (bidirectional
  inference) or from annotations.
- If the expected function type has exactly one parameter and the lambda
  declares none, the parameter is named `it`.
- The value of the last expression is the lambda's result. `return` returns from
  the lambda, never from the enclosing function. This is Rust closure semantics;
  Lyth has no non-local returns because lambdas are ordinary Go closures.
- If the expected function type is fallible (declared `throws`, or a Go
  callback type returning `error`), the lambda body may use `?` and `throw`.
  A Lyth function type with an `error` result returns the error as a value
  (see [errors.md §6](errors.md#6-fallible-function-types-and-lambdas)).

### 5.9 Pointers and channels **[v0.1]**

`&x` takes an address, only of mutable storage (a variable, or a field or
element reached through a mutable path) or of a construction `&T(...)`; the
address of a parameter or other read-only name is E0503.
`*p` dereferences.

**Channels.** Channel operations are methods of channel types that lower to
Go's operators ([ADR 0024](../adr/0024-channel-operations.md)):

| Lyth | Go | Needs |
|---|---|---|
| `make(chan T)`, `make(chan T, n)` | `make(chan T)`, `make(chan T, n)` | |
| `ch.send(v)` (statement) | `ch <- v` | `chan T` or `chan<- T` |
| `ch.receive()` | `<-ch` | `chan T` or `<-chan T` |
| `v, ok := ch.receive()` | `v, ok := <-ch` | `chan T` or `<-chan T` |
| `ch.close()` | `close(ch)` | `chan T` or `chan<- T` |

Semantics are Go's: receiving from a closed channel yields the zero value
with `ok` false, sending on a closed channel panics, and a nil channel blocks
forever. `for v in ch` receives until the channel is closed.

- `ch.receive()` has the element type `T`, or `T?` when `T` has no zero value
  (a non-null pointer, interface, function, or channel), because a closed
  channel yields nil: `job := jobs.receive() ?: break`. The comma-ok form
  gives the same type and a `bool`.
- `ch.send(v)` has no value; it is a statement.
- `go` and `defer` take a function call, which a send or receive is not
  (E0216): write `go { ch.send(v) }`. `defer ch.close()` is a call and works.
- Channels from Go APIs have the same methods (`ctx.Done().receive()`,
  `ticker.C.receive()`). Go's own `<-` is E0008 with the method spelling.

```ebnf
Select       = "select" "{" { SelectBranch | "else" "->" Body NEWLINE } "}" .  (* at most one else *)
SelectBranch = ( [ identifier [ "," identifier ] ":=" ]
                 Expression ".receive" "(" ")"
               | Expression ".send" "(" Expression ")" ) "->" Body NEWLINE .
```

`select` is a statement. It evaluates every branch's channel and sent value
in order, waits until one of the operations can proceed, and runs that
branch; `else` runs when none can (Go's `default`). A receive branch may bind
the value (`v := ch.receive() -> ...`), or the value and `ok`
(`v, ok := ch.receive() -> ...`), for its body; a send branch binds nothing
(E0216), and a branch that is not a channel operation is E0216. As in `when`,
`break` inside a branch exits the enclosing loop. A `select` whose branches
all end in `return`, `throw`, or another terminating statement terminates, so
nothing needs to follow it.

```kotlin
import "time"

func main() {
    results := make(chan int, 3)
    for i in 1..3 {
        go { results.send(i * i) }
    }
    sum := 0
    for _ in 1..3 {
        sum += results.receive()
    }
    results.close()
    _, open := results.receive()

    never := make(chan string)
    select {
        s := never.receive() -> println("got", s)
        time.After(10 * time.Millisecond).receive() -> println("timeout")
    }
    println(sum, open)
}
```

### 5.10 Type tests and casts **[v0.1]**

```ebnf
TypeTest = Expression ( "is" | "!is" ) Type .
Cast     = Expression ( "as" | "as?" ) Type .
```

The operand must have an interface type (`any`, `error`, `io.Reader`, a Lyth
`interface`), because only interface values hold values of different dynamic
types (E0209 otherwise, and for impossible tests: a concrete type that does not
implement the interface). The tested type must be non-null (E0216); a null
operand never matches.

| Form | Meaning | Lowers to |
|---|---|---|
| `x is T` | `x` holds a `T` | `_, ok := x.(T)` |
| `x !is T` | `x` does not hold a `T` | `!ok` |
| `x as T` | `x` as a `T`; panics if it is not one | `x.(T)` |
| `x as? T` | `x` as a `T`, or null | `t, _ := x.(T)` (boxed for value types) |

A successful `is` test **smart-casts** a variable to `T` exactly like a null
check, under the same conditions (a parameter, `self`, or a local the function
never reassigns, types.md §5.3): in the guarded branch, in the clauses of a
`when x { is T -> ... }`, and after `if x !is T { return }`. The lowering binds
the cast value once, as idiomatic Go does:

```kotlin
if err is *NotFound { log(err.Key) }        // if nf, ok := err.(*NotFound); ok { log(nf.Key) }
when s { is Circle -> s.R; else -> 0.0 }    // switch s := s.(type) { case Circle: ... }
```

Type tests see a value's dynamic type only; they do not unwrap errors. To test
for an error anywhere in a wrapped chain, use a typed `catch`
([errors.md §4.1](errors.md#41-catch)), `errors.AsType[*NotFound](err)`
(Go 1.26), or `errors.Is`.

### 5.11 Membership tests **[v0.1]**

```ebnf
InTest = Expression ( "in" | "!in" ) Expression .
```

`x in c` tests whether `c` contains `x`, and `x !in c` is its negation:

| `c` | `x in c` holds when | Lowers to |
|---|---|---|
| `a..b`, `a..<b` | `a <= x <= b`, `a <= x < b` | `x >= a && x <= b`, `x >= a && x < b` |
| `[]T`, `[N]T` | an element equals `x` | `slices.Contains(c, x)` |
| `map[K]V` | `x` is a key | `_, ok := c[x]` |
| `string` | the `string` or `rune` `x` occurs in `c` | `strings.Contains(c, x)`, `strings.ContainsRune(c, x)` |

`x` must have the element type: the bounds' ordered type for ranges, the
comparable element type for lists and arrays, the key type for maps (E0201,
E0209). `x` is evaluated first, then `c`, each once. In a `when` with a subject,
`in c` tests the subject: `when code { in 200..299 -> "ok" }`. Ranges exist
only in `for` loops and membership tests.

## 6. Evaluation order **[v0.1]**

Lyth evaluates operands, arguments, and collection-literal elements left to
right, completely. This is stricter than Go, which leaves the order of variable
reads relative to calls unspecified. When the generator must hoist part of an
expression into statements (e.g. for `?`, `catch`, `if`-expressions), it spills
preceding operands with side effects into temporaries so that the order
survives. Expressions that need no hoisting are emitted unchanged.

## 7. Builtins and prelude **[v0.1]**

Go's builtin functions keep their names and semantics: `len`, `cap`, `append`,
`copy`, `delete`, `make`, `new`, `panic`, `recover`, `min`, `max`, `clear`,
`close`. `print`/`println` write to standard output and lower to
`fmt.Print`/`fmt.Println`; Go's stderr builtins are not exposed. The prelude adds a
deliberately small set of methods on built-in types that lower to the standard
library. Each one replaces a common multi-line Go idiom:

| Lyth | Go | Since |
|---|---|---|
| `s.toInt()` / `s.toInt64()` / `s.toFloat64()` / `s.toBool()` (fallible) | `strconv.Atoi`/`ParseInt(s,10,64)`/`ParseFloat(s,64)`/`ParseBool` | v0.1 |
| `err.wrap("context")` | `fmt.Errorf("context: %w", err)` | v0.1 |

Everything else belongs in libraries. Lyth's standard library is imported as
`lyth/<name>` and compiled with the program (ADR 0030); its first package,
`lyth/coll`, provides collection operations as extension functions
(`todos.Filter { !it.Done }.Map { it.Title }`).

## 8. Mapping summary (normative for code generation)

### 8.1 Types

| Lyth | Go |
|---|---|
| `int int8 int16 int32 int64 uint uint8 uint16 uint32 uint64 uintptr` | the same |
| `float32 float64 complex64 complex128` | the same |
| `byte` (= `uint8`), `rune` (= `int32`), `bool`, `string` | the same |
| `any`, `error`; `comparable` (constraints only) | the same |
| `unit` (empty result in function types) | no result |
| `[]T`, `map[K]V`, `[N]T`, `chan T`, `chan<- T`, `<-chan T` | the same |
| `*T`, `*T?` | `*T` |
| `T?` for value types `T` | `*T` (boxed optional) |
| `I?` for interfaces, `((A) -> R)?` | same Go type, may be nil |
| `(A, B) -> R`, `(A) -> R throws` | `func(A, B) R`, `func(A) (R, error)` |

### 8.2 Declarations

| Lyth | Go |
|---|---|
| `func F(x: int): int throws` | `func F(x int) (int, error)` |
| `func *T.m()` | `func (t *T) m()` |
| `func F[T: cmp.Ordered](x: T)` | `func F[T cmp.Ordered](x T)` |
| `struct S { A: int }` | `type S struct { A int \`json:"a"\` }` |
| `interface I { M(): int }` | `type I interface { M() int }` |
| `var x = 1` (top level) | `var x = 1` |
| `x := 1`, `x: T` (local) | `x := 1`, `var x T` |
| `const N = 10` | `const N = 10` |
| `typealias H = (int) -> int` | `type H = func(int) int` |
| `struct S : I { ... }` | `type S struct { ... }` and `var _ I = S{}` |
| `func f(n: int = 1)`, called `f()` | `func f(n int)`, called `f(1)` |

### 8.3 Hygiene

Generated temporaries and renamed identifiers never capture or shadow
user-visible names. The generator keeps a scope-aware name allocator: a Lyth
local keeps its name in Go unless that would collide with a Go keyword, a Go
predeclared identifier, an import qualifier, or a package-level name used in the
same function. In those cases it gets a numeric suffix.

## 9. Tests **[v0.1]**

Test code lives in files ending in `_test.lyth`, in the package under test
(external test packages, `package foo_test`, are **[planned]**). They are
compiled only by `lyth test` (and checked by `lyth check`, `lyth vet`, `lyth gen`),
never by `lyth build` or `lyth run`, and lower to `x.lyth_test.go`.

In a test file, a top-level function whose name go test recognizes keeps that
name and must have go test's signature:

| Name | Signature | May throw |
|---|---|---|
| `TestXxx` | `func TestXxx(t: *testing.T)` | yes |
| `BenchmarkXxx` | `func BenchmarkXxx(b: *testing.B)` | yes |
| `FuzzXxx` | `func FuzzXxx(f: *testing.F)` | yes |
| `ExampleXxx` | `func ExampleXxx()` | no |
| `TestMain` | `func TestMain(m: *testing.M)` | no |

`Xxx` is empty or starts with a character that is not a lower-case letter, as
in Go. A wrong signature is an error (E0216). When a test declared `throws`
throws, the test fails with the error message, reported at the test's
declaration line. Other failures (`t.Errorf`, panics) report the `.lyth` line
where they happen.

```kotlin
import "testing"

func TestParse(t: *testing.T) throws {
    n := parse("42")?
    if n != 42 {
        t.Fatalf("parse = %d", n)
    }
}
```

Hand-written Go test files (`_test.go`) may sit beside `.lyth` files and use the
package's Go API. See [ADR 0014](../adr/0014-testing.md).
