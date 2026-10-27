# Lyth Language Specification

This is the normative description of Lyth's syntax and the semantics that are not
about types. Types, inference, nullability, and mutability are specified in
[types.md](types.md); error handling in [errors.md](errors.md). A section or rule tagged **[planned]**
is designed but not implemented yet; everything else is implemented in the
current compiler, with tests.

Notation: EBNF as in the Go specification. `|` alternation, `()` grouping,
`[]` option, `{}` repetition. Terminals are in double quotes.

## 1. Source files and lexical structure

### 1.1 Files

A source file is UTF-8 text with the extension `.lyth`. A byte order mark is
rejected. All files in one directory belong to one package (as in Go).

File names follow Go's build-constraint conventions so generated files keep
them: `foo_linux.lyth` compiles only for Linux, and `foo_test.lyth` is a test file.
See [compiler.md §6.2](../design/compiler.md#62-generated-file-names).

### 1.2 Comments

`// line comment` and `/* block comment */` (not nested). A run of line comments,
each on a line of its own, immediately preceding a declaration, struct field, or
interface method, enum entry, or the package clause is its doc comment and is copied
to the generated Go declaration so `go doc` works on Lyth packages. A comment after
code on the same line is not a doc comment. The other comments are copied too: a
comment after code goes after the Go code of that line, a comment on its own line
inside a function goes before the Go code of the next line, and a block of comments
that a blank line separates from what follows (a copyright header, a section heading)
stays separate. Comments inside an expression may move to the end of their line.

**Directives.** A line comment `//go:name args` at the start of a line is a Go
directive. `//go:build` lines before the package clause and imports are build
constraints (§1.1). `//go:generate` lines are run by `lyth generate`. The other
directives apply to the declaration directly below them (doc comment lines may
come between) and are copied to the generated Go declaration:

- `//go:embed patterns` above a package-level `var` that has a type and no
  initializer, of type `string`, `[]byte`, or `embed.FS` (§3.5), as in Go's
  `embed` package: the build fills the variable with the files of the package
  directory that the patterns match. The generated file imports `embed`.
- `//go:noinline`, `//go:nosplit`, `//go:norace`, and `//go:nocheckptr` above a
  function or method.

Any other directive, a directive above another kind of declaration, and one
that no declaration follows are errors (E0008, E0216, E0601): Go would ignore
them, and an ignored `//go:embed` leaves its variable empty.

### 1.3 Newlines and statement termination

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

### 1.4 Identifiers

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

### 1.5 Keywords

Hard keywords (cannot be identifiers):

```
as break catch chan const continue defer else enum false for func go if
import interface is map null package return sealed select self struct
throw throws true type typealias var when while
```

`in` is a contextual keyword ([ADR 0044](../adr/0044-remaining-spellings.md)).
After an operand it is the membership operator (`x in xs`, `x !in xs`), in a
`for` header it separates the bindings from the iterated value
(`for x in xs`), and at the start of a `when` branch it is a membership
condition (`in 200..299 ->`). Everywhere else it is an ordinary name: a
field, parameter, local, loop variable, lambda parameter, or named argument
(`func stage(in: <-chan int)`, `tc.in`, `parseCase(in = "x")`). A variable
named `in` at the start of a `when` branch condition must be parenthesized.

`var` declares package-level variables only (§3.5). `self` is the receiver of
a method or extension function (§3.2) and can never be declared. `val` is
reserved only so that code in the old syntax gets a precise error (E0008, with
the `:=` or `var` form that replaces it), and so is `typealias` (E0008, with
the edit to `type`, §3.6). `pub` and `private` are reserved for
the same reason: Lyth has no visibility keywords (§3.1). `fun` and `this` are
not keywords; a declaration beginning with `fun` is E0008 with the edit
`func`, and `this` is an ordinary name: only where it is undeclared inside a
method is it E0008 with the edit `self`.

### 1.6 Operators and punctuation

```
+  -  *  /  %  &  |  ^  &^  <<  >>  &&  ||  !  ==  !=  <  <=  >  >=
=  +=  -=  *=  /=  %=  &=  |=  ^=  &^=  <<=  >>=  ++  --
:=  .  ?.  ?  ?:  !!  ..  ..<  ->  ...  ,  ;  :  @
(  )  [  ]  {  }
```

`:=` declares local variables (§4.1). Go's `<-` operator does not exist: `<-`
appears only in the channel types `<-chan T` and `chan<- T`, and `<-ch` or
`ch <- v` is E0008 with the method spelling (§5.9).

### 1.7 Literals

```ebnf
int_lit    = decimal | "0x" hex_digits | "0o" octal_digits | "0b" binary_digits .
float_lit  = decimal "." decimal [exponent] | decimal exponent .
imag_lit   = ( decimal | float_lit ) "i" .
rune_lit   = "'" ( unicode_char | escape ) "'" .
string_lit = '"' { string_char | escape | template } '"' .
template   = "${" Expression "}" .
raw_string = "`" { any char except "`" } "`" .
```

- Digits may be separated by `_` (`1_000_000`). A number starts with a digit
  (no `.5`).
- **Complex numbers** are Go's: the types `complex64` and `complex128`,
  imaginary literals (`2i`, `1.5i`), and the builtins `complex(re, im)`,
  `real(c)`, and `imag(c)`. They take `+ - * /` and `== !=`, not ordering
  (E0209); `math/cmplx` has the rest.
- Escapes: `\n \r \t \a \b \f \v \\ \" \' \$ \0 \uXXXX \U00XXXXXX \xHH`.
  As in Go, `\xHH` is one byte in a string (`"\xff"` is the byte 0xff) and the
  code point U+00HH in a rune (`'\xff'` is 255).
- **String templates.** `${expr}` inside a `"..."` literal interpolates. There is
  exactly one template syntax. Any other `$` is text: `"$1"`, `"US$ 5"`, and
  `$name` itself, so MongoDB's `"$set"`, JSON Schema's `"$ref"`, and
  PostgreSQL's dollar quotes (`$body$`) need no escaping. The one exception
  catches the habit `"$name"` from other languages: when `name` is a variable,
  parameter, or constant in scope where the string is written, `$name` is a
  compile error (E0006), whose hints give both `${name}` (interpolate it) and
  `\$name` (keep the dollar). Neither edit is applied by `lyth fix`, because
  they mean different things ([ADR 0042](../adr/0042-dollar-names-in-strings.md)).
  So declaring a variable can turn a literal `$name` that matches it into an
  error; write `\$name` there. `\$` is a literal `$` everywhere.
  A value is formatted as `fmt`'s `%v` formats it (an `Error` method first,
  then `String`), except that a null value or optional prints `null`, an
  optional prints its value, and a `rune` prints as its character
  (`println(r)` prints its number, as Go does). Collections print as `fmt`
  prints them, so a `[]int?` shows its elements' pointers.
- **Raw strings** use backticks, exactly as in Go: no escapes, no templates, may
  span lines. They are also the syntax for struct tags.
- Integer and float literals are *untyped constants* (Go semantics).
- `true`, `false`, and `null` are keywords.

## 2. Packages and imports

```ebnf
SourceFile    = [ PackageClause ] { ImportDecl } { TopLevelDecl } .
PackageClause = "package" identifier NEWLINE .
ImportDecl    = "import" ( ImportSpec | "(" { ImportSpec NEWLINE } ")" ) NEWLINE .
ImportSpec    = [ identifier | "_" ] string_lit .
```

A package is a directory. The package clause is optional. When it is
omitted, the name is `main` if a file in the package declares a top-level
`func main()`, otherwise the directory name (which must then be a valid
identifier). A `_test.lyth` file whose package clause names the directory's
package with `_test` (`package shapes_test`) is an external test package, as
in Go: it imports the package under test and sees only its exported names.

Import paths are Go import paths, resolved by the Go toolchain
against the enclosing module's `go.mod`. An import may refer to a Go package or a
Lyth package; the importer cannot tell the difference and does not need to. The
default qualifier is the imported package's declared name (e.g. `pgx` for
`github.com/jackc/pgx/v5`), not the last path element. A name before the path
renames the qualifier, and `_` imports for side effects (database drivers), as in
Go: `import h "net/http"`, `import _ "github.com/jackc/pgx/v5/stdlib"`.
Dot-imports do not exist. Imports can be grouped as in Go, one per line:
`import ( "fmt"; h "net/http" )` written over several lines. The spelling before
[ADR 0044](../adr/0044-remaining-spellings.md), `"net/http" as h`, is E0008 with
the edit to `h "net/http"`.

Unused imports are a warning (W0002); the generated Go simply omits them.

## 3. Declarations

```ebnf
TopLevelDecl = FuncDecl | StructDecl | InterfaceDecl | SealedDecl
             | EnumDecl | VarDecl | ConstDecl | TypeAliasDecl .
```

### 3.1 Visibility and Go names

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

### 3.2 Functions

```ebnf
FuncDecl    = "func" [ Receiver "." ] identifier [ TypeParams ]
              "(" [ ParamList ] ")" [ ":" ResultType ] [ "throws" ] FuncBody .
TypeParams  = "[" TypeParam { "," TypeParam } "]" .
TypeParam   = identifier [ ":" Type ] .
Receiver    = [ "*" ] Type .
ParamList   = Param { "," Param } [ "," ] .
Param       = identifier ":" [ "..." ] Type [ "=" Expression ] .
ResultType  = Type | "(" Type "," Type { "," Type } ")" | "(" Named { "," Named } ")" .
Named       = identifier ":" Type .
FuncBody    = Block | "=" Expression .
```

- **Named results**, as in Go ([ADR 0047](../adr/0047-go-constructs-lyth-lacked.md)):
  `func split(n: int): (q: int, r: int)`, and with `throws`,
  `func *Writer.Write(buf: []byte): (n: int) throws`. The names are locals
  holding their zero values (a type without one is E0405), visible to
  `defer` blocks, which may assign them. A `return` with values sets and
  returns them; a bare `return` returns them as they are, and only a
  function with named results may write one (E0207 otherwise). They lower
  to Go's named results, the error of a `throws` function named `err`:

  ```kotlin
  func split(n: int): (q: int, r: int) {
      q = n / 2
      r = n % 2
      return
  }

  func main() {
      q, r := split(7)
      println(q, r)
  }
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
- Parameters are read-only: assigning one is E0501 and taking its address is
  E0503. A `:=` declaration (single or list) may name a parameter when its
  right side reads that parameter: `ctx, cancel := context.WithTimeout(ctx, d)`
  declares a new `ctx` that hides the parameter for the rest of the block,
  and the parameter itself is never assigned
  ([ADR 0039](../adr/0039-rebinding-parameters.md)). The new variable has the
  parameter's type (a different type is E0201: choose a new name), and no
  W0006 is reported. The generated line is Go's own; in the function's
  outermost block it assigns the parameter, unless a closure created before
  it captured the parameter, in which case the new variable gets a Go name of
  its own so the closure keeps seeing the parameter. A `:=` that names a
  parameter without reading it is E0005 (it would ignore the argument). Copy
  a parameter into a local to change it otherwise. `xs: ...int` declares a variadic
  parameter whose type inside the body is `[]int`; callers spread a slice
  with `xs...` (Go syntax).
- `throws` makes the function fallible: its Go signature gets a trailing
  `error` result. Semantics in [errors.md](errors.md).
- A parenthesized result list declares multiple results (a *tuple*), returned
  as in Go: `return a, b`. Tuples
  exist only as results and are consumed by destructuring (§4.2).
- `func main() throws` is allowed. The generated `main` prints the error to
  stderr and exits with status 1.
- `func init()` declares an initialization function, as in Go: after the
  package-level variables are initialized, the package's `init` functions run
  in the order they appear (files in name order), before `main`. A package may
  declare several. `init` takes no parameters, returns no results, has no type
  parameters, and cannot throw (E0216); nothing can call or refer to it
  (E0101). Another package-level declaration named `init` gets the Go name
  `init_`; a field or method named `init` (or `len`, `copy`, ...) keeps its
  name, since Go members do not clash with package-level names.
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

**Methods.** `func User.FullName(): string` declares a method with a
value receiver; `func *User.Rename(name: string)` a method with a pointer
receiver. The `*` decides Go's method set and whether the method sees the
caller's value or a copy, so it stays visible. Both lower to a Go receiver
named after the type (`func (u *User) Rename(name string)`). The receiver type
must be declared in the same package (Go rule). Inside the body, the keyword
`self` is the receiver, and the receiver's fields and methods are reached only
through it: `self.Name`, `self.validate()`. A bare name in a method resolves as
it would outside the method, so a field may share its name with a local, a
type, an import, or a package-level declaration. A bare name that is a member
of `self` and nothing else in scope is E0101 ("Name is a field of self; write
self.Name"), and so is one that before ADR 0038 meant the member, even where it
now also names something else, so no meaning changes silently. `lyth fix`
inserts `self.` where the name can mean only the member; where it also names
something valid in that position (a package-level variable, constant, or
function, an import before a dot, or a type that is called), the
error says so and the edit is a hint for the author
([ADR 0038](../adr/0038-members-through-self.md)). A called builtin is the
exception, since nothing else reaches it: `append(xs, x)` in a type with a
method `append` calls the builtin, with warning W0011 while code from before
ADR 0038 is migrated. A call of a field that is
not a function, which never compiled, means the other name:
`Config(Name = "x")` constructs the type, `max(a, b)` calls the builtin, and
`strings.Join(...)` calls the package when the field has no member `Join`.

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

**Extension functions.** The method syntax with any other receiver type
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

**Generic functions.** `func name[T: Constraint](...)` declares type
parameters, which map 1:1 to Go type parameters; `[T]` alone means
`[T: any]`. The constraint follows a colon, as every Lyth name-and-type pair
does; Go's `[T cmp.Ordered]` is E0008 with the edit that inserts it. Type
arguments are inferred at calls or given as `name[int](...)`. The rules are in
[types.md §10](types.md#10-generics).

### 3.3 Structs

```ebnf
StructDecl = "struct" identifier [ TypeParams ] [ ":" Type { "," Type } ] "{" { FieldDecl } "}" .
FieldDecl  = ( identifier ":" Type | EmbeddedField ) [ raw_string ] NEWLINE .
EmbeddedField = [ "*" ] TypeName [ TypeArgs ] [ "?" ] .
```

```kotlin
struct User {
    ID: int64
    Name: string
    Email: string?
    passwordHash: string
}
```

- Field visibility follows §3.1: `Name` is exported, `passwordHash` is not.
  Reflection-based Go libraries (`encoding/json`, database scanners) see only
  exported fields.
- A raw-string tag is emitted verbatim, except that a `json` tag without a
  name (`json:",string"`, `json:",omitempty"`) gets the automatic name, and on
  a `T?` field also `omitzero` unless it says `omitempty` or `omitzero`. If the
  tag has no `json` key, Lyth prepends one for exported fields, and a field of
  a nullable type (`T?`) also gets `,omitzero`, so a null optional is left out
  of the output rather than written as `null`. Unexported fields get no tags
  ([ADR 0013](../adr/0013-struct-fields-and-json.md),
  [ADR 0022](../adr/0022-capitalization-is-visibility.md),
  [ADR 0036](../adr/0036-automatic-json-tags.md)).
- The automatic key is the field name in lower camel case: its first word is
  lowered and the rest is kept. The first word is the leading run of capitals
  without its last letter when a lowercase letter follows (`HTTPServer` →
  `httpServer`), the whole run when it is followed by `s` and the end of the
  name or another capital (`IDs` → `ids`, `UserIDs` → `userIDs`), a listed
  initialism that the run starts with when a lowercase letter or digit follows
  it (`IPv4` → `ipv4`, `UTF8Name` → `utf8Name`), and otherwise the leading
  capital (`Name` → `name`). The list is golint's common initialisms (ACL,
  API, ASCII, CPU, CSS, DNS, EOF, GUID, HTML, HTTP, HTTPS, ID, IP, JSON, LHS,
  QPS, RAM, RHS, RPC, SLA, SMTP, SQL, SSH, TCP, TLS, TTL, UDP, UI, UID, UUID,
  URI, URL, UTF8, VM, XML, XMPP, XSRF, XSS).
- When the rule splits a run of capitals after its first letter (`ETag` →
  `eTag`, `OAuthToken` → `oAuthToken`), the name may be one mixed-case word or
  two, so the field warns (W0008) until it has a tag: `json:"etag"`, or
  `json:"eTag"` to keep the automatic key.
- Two fields with the same json key (`IDs` and `Ids` are both `ids`) warn
  (W0008): `encoding/json` would leave both out.

**Embedding.** A type alone on a line of the struct body embeds it, as in Go
([ADR 0043](../adr/0043-struct-embedding.md)):

```kotlin
import "net/http"

struct statusRecorder {
    http.ResponseWriter
    status: int
}

func *statusRecorder.WriteHeader(code: int) {
    self.status = code
    self.ResponseWriter.WriteHeader(code)
}
```

- The embedded type is a type name, optionally qualified or instantiated
  (`T`, `pkg.T`, `Page[int]`), a pointer to one (`*T`), or a nullable one
  (`T?`). A type parameter, a pointer to an interface, and any other type are
  errors (E0216).
- The field's name is the type's name without package, `*`, or type
  arguments (`ResponseWriter`). It is exported when that name is, and it is
  constructed by that name (`statusRecorder(ResponseWriter = w)`) or in its
  place among positional arguments.
- Promotion follows Go exactly (depth, ambiguity, method sets): promoted
  fields and methods are reached through a value or through `self`
  (`self.Header()`), keep their Lyth types, and count when the struct is used
  as an interface. A method declared on the outer type hides a promoted one.
  An ambiguous selector is E0205.
- An embedded pointer, interface, function, or channel type has no zero
  value, so it must be given at construction. A nullable embedded type
  (`Store?`, for partial test fakes) may be left null: its promoted methods
  still make the struct implement interfaces, and a call through an interface
  reaches the null value and panics, as in Go. A direct access in Lyth code
  is E0401 ("Delete is promoted through the nullable field Store").
- An embedded field gets no automatic `json` tag, so `encoding/json` flattens
  an embedded struct as in Go; a tag on the embedding line
  (``Timestamps `json:"ts"` ``) nests it under that key.
- Embedding a member of a sealed interface does not make the outer struct a
  member (§3.7): converting it to the interface is E0201.
- Go's `Name Type` field spelling is E0008, with the edit that inserts `:`.

**Construction.** A struct type is constructed by calling it:

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
non-nullable pointer, see [types.md §6](types.md#6-zero-values)). Construction always
generates a keyed Go composite literal, so it is robust to field reordering and
passes `go vet`'s composites check. `&T(...)` takes the address of a fresh value.

**Declared conformance.** `struct Circle : Shape, fmt.Stringer { ... }`
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

### 3.4 Interfaces

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
`interface Container[T] { Get(): T }`. An interface may hold a type set, as
in Go ([ADR 0047](../adr/0047-go-constructs-lyth-lacked.md)):
`interface Number { ~int | ~int64 | ~float64 }`, where `~T` stands for every
type whose underlying type is `T`; a line with a type alone (`interface Int {
int }`) is a set of one. Such an interface is a constraint only (E0216
elsewhere), and a constraint may be a set itself: `[T: int | float64]`. The
operators of a type parameter are those every type of its set has.

### 3.5 Variables and constants

```ebnf
VarDecl    = "var" ( VarSpec | "(" { VarSpec NEWLINE } ")" ) NEWLINE .
VarSpec    = identifier ( ":" Type [ "=" Expression ] | "=" Expression ) .
ConstDecl  = "const" ( ConstSpec | "(" { ConstSpec NEWLINE } ")" ) NEWLINE .
ConstSpec  = identifier [ ":" Type ] "=" Expression | identifier .
```

A top-level `var` declares a Go package-level variable, with Go's dependency-
ordered initialization. `var x: T` without an initializer has the zero value
of `T`, which must have one (E0405, types.md §6): `var mu: sync.Mutex`,
`var cache: map[string]int`. A `//go:embed` directive above such a variable
fills it with files instead (§1.2). It is mutable: any code in the package (and, when it
is exported, any importing package) may assign it, so it never smart-casts
([types.md §5.3](types.md#53-using-nullable-values)). A top-level `val` is
E0008 with the edit `var`. `const` declares a Go constant: the initializer must
be a constant expression, untyped unless a type is given
(`const X: int64 = 5`); assigning one is E0501. `var` is package-level only:
inside a function, local variables are declared with `:=` (§4.1). `const` may
also appear in a function body, as in Go; a local constant is in scope from its
declaration to the end of the block and may refer to earlier constants.

**Groups.** As in Go ([ADR 0047](../adr/0047-go-constructs-lyth-lacked.md)),
`var ( ... )` groups package-level variables and `const ( ... )` groups
constants (also in a function), one per line, each written as on its own
(`A: uint = 1 << iota`, `counter: int`). A comment above the group is its doc.
In a constant declaration `iota` is the entry's index in its group (0 outside
one), and an entry with only a name repeats the type and expression of the
last entry with a value, with its own `iota`:

```kotlin
import "fmt"

// Formatting can be controlled with these flags.
const (
    FilterHTML: uint = 1 << iota
    StripEscape
    AlignRight
)

func main() {
    fmt.Println(FilterHTML, StripEscape, AlignRight)
}
```

A repeated entry whose value does not fit its type is reported at that entry
(E0215). Each entry declares one name (`A, B = 1, 2` is E0008). `iota`
outside a constant declaration is E0101. Enumerations whose values have
names on the wire and exhaustive `when` are `enum` (§3.7); groups are for
flags, sizes, and Go-compatible names.

### 3.6 Defined types and type aliases

```ebnf
TypeDefDecl   = "type" identifier [ TypeParams ] Type NEWLINE .
TypeAliasDecl = "type" identifier [ TypeParams ] "=" Type NEWLINE .
```

Both use Go's spelling ([ADR 0037](../adr/0037-type-declarations.md)).

**Defined types.** `type UserID int64` declares a new type with the
underlying type `int64`, as in Go: a value of another type (`OrderID`,
`int64`) needs an explicit conversion (`UserID(n)`, `int64(id)`), untyped
constants are assignable (`id: UserID = 5`, `const Admin: Role = "admin"`),
and the operators of the underlying type apply (`price * 2`, `tags[0]`,
`for t in tags`). Methods are declared as on structs (`func Cents.Euros()`,
`func *Tags.Add(t: string)`), except on a defined pointer type, where Go
forbids them. A defined type may be generic: `type Set[T: comparable]
map[T]struct{}` (the bracket must follow the name directly; with a space,
`type Grid [3]int` is an array type). A defined type takes the nullability
and zero-value rules of its underlying type: one over a function, pointer, or
channel type is non-null and has no zero value, and `T?` is its nullable form;
one over a slice or map is never null. The underlying type cannot be a
struct, interface, enum, or nullable type: structs and interfaces have their
own declarations (`struct User { ... }`), and Go's `type User struct { ... }`
is E0008 with the edit to `struct User`. A defined type declared over itself
is E0216. Go's named function types follow the same rule as Lyth's: a field
of type `context.CancelFunc` must be given at construction, or declared
`context.CancelFunc?`.

**Type aliases.** `type Handler = (Ctx) -> unit throws` gives an existing
type a second name; it does not create a new type, so values convert freely
and the alias has the methods of the aliased type. It lowers to a Go alias
declaration (`type Handler = func(Ctx) error`). Aliases are how Lyth names
function types and long instantiations without the cost of a new type. An
alias may refer to other aliases but not to itself (E0216). An alias may be
generic, as in Go 1.24: `type Set[T: comparable] = map[T]bool`, used with its
type arguments (`Set[string]`, which must satisfy the constraints, E0213).
The spelling before ADR 0037, `typealias Handler = ...`,
is E0008 with the edit to `type Handler = ...`.

### 3.7 Enums and sealed interfaces

```ebnf
EnumDecl   = "enum" identifier [ ":" Type ] "{" Entry { ( "," | NEWLINE ) Entry } [ "," ] "}" .
Entry      = identifier [ "=" string_lit ] .
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
  value and `Color(n)` converts back, as in Go.
- An enum may have methods (`func Color.hex(): string`) but not one named
  `String`, which is generated (E0102).

**Enums on the wire.** An enum travels by name: Lyth generates `MarshalText`
and `UnmarshalText`, which write an entry's name under the automatic `json`
key rule (§3.3: `Active` → `active`, `PendingReview` → `pendingReview`) and
accept only those names. `encoding/json` (v1 and v2), `flag.TextVar`, and
`log/slog` use them, so `{"status":"pendingReview"}` is the JSON form, and
decoding `7` or `"Gold"` is an error rather than a value outside the set.
`fmt` and string templates keep `String()`, the entry's name as written. Two
entries with the same wire name (`Id`, `ID`) are E0102
([ADR 0035](../adr/0035-enums-on-the-wire.md)).

- An enum that declares `MarshalText` and `UnmarshalText` itself gets neither
  generated; declaring only one is E0216. To keep numbers in JSON, declare
  `MarshalJSON` and `UnmarshalJSON`, which `encoding/json` prefers.
- Databases store a plain enum's number in an integer column. Text columns
  use a string-backed enum.

**String-backed enums.** `enum Plan: string { Free = "free", Pro = "pro" }`
gives every entry a string value and lowers to Go's own idiom: `type Plan
string`, typed constants `PlanFree Plan = "free"`, a `String()` that returns
the value, and an `UnmarshalText` that accepts exactly the declared values.
`encoding/json`, pgx, and `database/sql` read and write the value with no
other methods, and `string(p)` gives it.

- Every entry needs a distinct string literal (E0216, E0102). `string` is the
  only backing type; numeric codes are a defined type with constants (§3.6).
- The zero value is `""`, which is a value of the enum only when an entry
  declares it. Without such an entry the enum has no zero value in Lyth
  (types.md §6): a field of the type must be given at construction, and
  `p: Plan` needs an initializer (E0405). Reflection can still produce `""`
  (a JSON field left out), which an exhaustive `when` rejects with its panic;
  declare input fields `Plan?` when they are optional.

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
as a value or as a statement that terminates. A `when` over a nullable enum or
sealed interface need not cover anything as a statement, and is exhaustive when
it covers every entry or member type and `null`. Inside such a `when`, entries
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
Statement  = ShortVarDecl | TypedVarDecl | ConstDecl | StructDecl | Assignment | IncDec
           | Return | Throw | Break | Continue | Defer | Go | For | While | Select | Expression .
```

A struct declared in a function body (`StructDecl`, §3.3) is a Go local type:
it is in scope from its declaration to the end of the block, may refer to
itself through pointers, optionals, slices, and maps, and is constructed and
used like any struct. It has no methods, since methods are declared at package
level, and no type parameters or declared interfaces (E0601, E0216). Table
tests declare their case type this way:

```kotlin
func check(parse: (string) -> int) {
    struct tc {
        in: string
        want: int
    }
    for c in [tc(in = "1", want = 1), tc(in = "22", want = 22)] {
        println(c.in, parse(c.in) == c.want)
    }
}
```

### 4.1 Local declarations

```ebnf
ShortVarDecl = BindingName { "," BindingName } ":=" ExprList .
TypedVarDecl = identifier ":" Type [ "=" Expression ] .
BindingName  = identifier | "_" .
ExprList     = Expression { "," Expression } .
```

- `x := e` declares a new variable in the current scope, with the type of `e`
  (an untyped constant takes its default type, [types.md §9](types.md#9-type-inference)).
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
  or `name: Type` form. `const` declares a local constant (§3.5).
- A declaration in an inner block may hide a variable or parameter of an
  enclosing scope, as in Go. It draws warning W0006, which catches the
  shadowed `err` that `x, err := f()` in an inner block leaves unchanged in
  Go. In a method, a local that hides a field of `self` draws W0006 as well
  (§3.2).
- An unused local is warning W0001. Go would reject the generated code, so the
  generator emits `_ = x` to keep it valid.

### 4.2 Destructuring

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

### 4.3 Assignment

```ebnf
Assignment = Target { "," Target } "=" ExprList
           | Expression assign_op Expression .
Target     = Expression | "_" .
```

`lhs = rhs` and compound forms `+= -= *= /= %= &= |= ^= &^= <<= >>=`. The left side
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

### 4.4 Control flow

```ebnf
If       = "if" [ SimpleStmt ";" ] Expression Block [ "else" ( If | Block ) ] .
While    = [ Label ] "while" Expression Block .
For      = [ Label ] "for" BindingName [ "," BindingName ] "in" Expression Block
         | [ Label ] "for" [ SimpleStmt ] ";" [ Expression ] ";" [ SimpleStmt ] Block .
SimpleStmt = LocalDecl | Assignment | IncDec | Call .
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
- `if v := f(); cond { ... }` declares `v` for the whole `if`, its `else if`s
  and its `else`, as in Go ([ADR 0047](../adr/0047-go-constructs-lyth-lacked.md)):
  `if v, ok := m[k]; ok { ... }`, `if r := recover(); r != null { ... }`.
- `while c { ... }` loops while `c` holds; an endless loop is
  `while true { ... }`. `for c { ... }` is a syntax error, since `x in xs` is
  also a membership test.
- Go's three-part loop is `for` too ([ADR 0047](../adr/0047-go-constructs-lyth-lacked.md)):
  `for i := 0; i < n; i++ { ... }`, `for i, j := 0, n - 1; i < j; i, j = i + 1, j - 1 { ... }`.
  The init and post are simple statements (the post cannot declare, E0216),
  any part may be empty, and `continue` runs the post statement. Its
  variables are fresh per iteration, as in Go 1.22. `for ; ; { }` is E0008
  with the edit to `while true`; counting by one reads best as
  `for i in 0..<n`.
- `for x in e` iterates like Go's `range`, but binds the element first:
  - `[]T`/`[N]T`: `x` is the element; `for i, x in xs` gives index and element.
  - `map[K]V`: `for k, v in m`; `for k in m` gives keys. Iteration order is
    Go's, i.e. unspecified.
  - `string`: runes; `for i, r in s` gives byte offset and rune.
  - `chan T`: values until the channel is closed.
  - ranges `a..b` (inclusive) and `a..<b` (exclusive) of integer type.
  - Go iterator functions: `iter.Seq[V]` (`for v in seq`) and
    `iter.Seq2[K, V]` (`for k, v in seq`; `for k in seq` gives the first
    value), or any `func(yield func(...) bool)`. They lower to Go's
    range-over-func, so `maps.Keys(m)`, `strings.Lines(s)`, and
    `slices.All(xs)` work directly.
  Go's `for i, x := range xs` is E0008; its hint gives the Lyth form (for
  `for _, x := range xs`, `for x in xs`). Its loop variables are read-only and
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
  must be handled with `catch` (`defer f.Close() catch {}`), or, in a function
  that throws, propagated with `?` (`defer f.Close()?`), which makes its error
  the function's when the function is not failing already
  ([errors.md §7](errors.md#7-defer-panic-and-recover)). A `go` call cannot
  return an unhandled error.

### 4.5 Expression statements

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
| 9 | `*` `/` `%` `<<` `>>` `&` `&^` | Go's multiplicative level |
| 10 | `as` `as?` | |
| 11 | prefix `-` `+` `!` `^` `&` `*` | |
| 12 | postfix `.` `?.` `?` `!!` call `[...]` `catch` | |

Arithmetic and bitwise levels match Go, so generated expressions need no extra
parentheses and Go readers' intuitions hold. Comparisons are split
so that `a < b == c < d` means `(a < b) == (c < d)`.

### 5.2 Primary expressions

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

Index and slice expressions are Go's: `xs[i]`, `m[k]`, `s[lo:hi]` with
either bound optional, and the full slice expression `xs[lo:hi:max]`, whose
capacity is `max - lo` (not for strings, E0209). A pointer to an array is
indexed and sliced like the array (`p[1:]`). A constant index or bound
outside an array or a constant string, a negative one, and `lo > hi` are
E0209, as Go rejects them.

Typed literals spell the type, as in Go: `[]int64{1, 2}`,
`map[string]any{"status": "ok", "count": 3}`, `[3]int{1, 2}` (the rest is
zero), and `[...]string{"a", "b"}`. Elements are expressions typed by the
element type (`[][]int{[1, 2]}`); Go's elided `{1, 2}` and indexed `{0: a}`
elements are E0008. In a header, the braces after a slice, array, or map type
are the literal's, as in Go: `for x in []int{1, 2} { ... }`.
Go's struct literal `Point{X: 1}` is E0008 with the construction call
`Point(X = 1)`, and Go's literal of a named map type `gin.H{"k": v}` is E0008
with the conversion `gin.H(["k": v])` (ADR 0032). A constant map key given twice is E0102. A list literal lowers to a Go
slice literal `[]T{...}`, which is non-nil even when empty, so it serializes as
`[]`, not `null`.

### 5.3 Calls, construction, conversion

`f(args)` is a function call, a struct construction (§3.3), or a conversion when
`f` names a non-struct type (`int64(x)`, `float64(n)`, `time.Duration(n)`,
`string(bytes)`, `[]byte(s)`).
As in Go, a call whose only argument is a call returning several values passes
those values as the arguments: `template.Must(t.Parse(s))` gives `Must` the
template and the error, and `show(pair())` the two results of `pair`. A
variadic function takes several results this way only when none is an error
(`sum(pair())`); `fmt.Println(strconv.Atoi(s))` stays E0210, so an error is
never printed instead of handled.
Conversions follow Go's rules exactly; a lambda converted to a named function
type takes its parameter types from that type
(`http.HandlerFunc({ w, r -> ... })`), and a collection literal converted to a
type is a literal of that type: `gin.H(["error": msg, "code": 7])` lowers to
`gin.H{"error": msg, "code": 7}`. A trailing lambda may follow the
argument list on the same line: `strings.FieldsFunc(s) { r -> r == ',' }`.
Explicit type arguments follow the name in brackets, `Max[int](xs)` or
`errors.AsType[*NotFound](err)`, and a trailing lambda may follow them
(`run[string] { "ok" }`): `x[A]` is an instantiation when `x` names a
generic function or type, and an index otherwise
([types.md §10](types.md#10-generics)).

**Named arguments.** Calls to functions and methods declared in Lyth may
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

### 5.4 Member access

`x.name` selects a field or method. Pointers are dereferenced automatically and
addresses are taken automatically for pointer-receiver calls, as in Go, subject
to mutability ([types.md §7](types.md#7-mutability)).

`x?.name` is the **safe call**: when `x` is null the result is null, otherwise
the member is accessed. When `x` is a *fallible result* (a call returning an
error), `x?.name` means `(x?).name`: propagate the error, then access the
member. See [errors.md §3.3](errors.md#33-chaining).

### 5.5 Null-handling operators

`a ?: b` evaluates to `a` if it is non-null, otherwise to `b` (evaluated only
when needed). `b` may be a diverging expression: `?: return x`, `?: throw e`,
`?: continue`. `x!!` asserts that `x` is non-null; if it is null, the program
panics with a message naming the source position. Details are in
[types.md §5](types.md#5-nullability).

### 5.6 Error operators

Postfix `e?` propagates an error. `e catch (err) { ... }` handles it locally. See
[errors.md](errors.md).

### 5.7 `if` and `when` expressions

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
([§3.7](#37-enums-and-sealed-interfaces)).

A `when` whose conditions are all `is` tests (and `null` over an interface
subject, Go's `case nil`) lowers to a Go type switch;
`when r := f() { ... }` binds the subject for the branches. Other `when`s lower to a Go `switch`, or
to an `if`/`else` chain when a condition needs statements (a `?`, a mix of `is`
and value conditions). When an `if` or `when` value is returned, each branch
returns directly.

### 5.8 Lambdas

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
- A lambda with several results returns them with `return a, b`, or ends in
  a call that returns exactly them (`each(xs) { pair(it) }`).
- A lambda may return a lambda: `{ a: int -> { b: int -> a * b } }`. A `{` at
  the start of a statement is a lambda when parameters and `->` follow it,
  and a block otherwise.
- An annotated parameter must accept every value the caller passes: where
  the expected type passes `*User?`, `{ u: *User -> ... }` is E0201.
- If the expected function type is fallible (declared `throws`, or a Go
  callback type returning `error`), the lambda body may use `?` and `throw`.
  A Lyth function type with an `error` result returns the error as a value
  (see [errors.md §6](errors.md#6-fallible-function-types-and-lambdas)).

### 5.9 Pointers and channels

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
  (a non-null pointer, interface, function, or channel, or a struct holding
  one; types.md §6), because a closed channel yields Go's zero value:
  `job := jobs.receive() ?: break`. The comma-ok form gives the same type and
  a `bool`, and so does a binding in a `select` branch. For a struct or enum
  without a zero value, the received value is boxed: null once the channel is
  closed.
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
nothing needs to follow it. A null channel in a branch (`off?.receive()`,
`off?.send(v)`) is never ready, as Go's nil channel, which turns a branch off;
its binding has the element type.

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

### 5.10 Type tests and casts

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

A successful `is` test **smart-casts** a variable to `T` like a null check,
for a parameter, `self` (in an extension of an interface:
`if self is Square { self.S }`), or a local the function never reassigns
(types.md §5.3): in the guarded branch, in the clauses of a
`when x { is T -> ... }`, and after `if x !is T { return }`. The lowering binds
the cast value once, as idiomatic Go does:

```kotlin
if err is *NotFound { log(err.Key) }        // if nf, ok := err.(*NotFound); ok { log(nf.Key) }
when s { is Circle -> s.R; else -> 0.0 }    // switch s := s.(type) { case Circle: ... }
```

Type tests see a value's dynamic type only; they do not unwrap errors. To test
for an error anywhere in a wrapped chain, use a typed `catch`
([errors.md §4.1](errors.md#41-catch)), `errors.AsType[*NotFound](err)`
(Go 1.26), or `errors.Is`. Because errors usually arrive wrapped, a type test
(`is`, `!is`, `as`, `as?`, or a `when` branch) of a type that implements
`error` on a value whose static type is `error` draws warning W0007, whose
hints give the typed `catch` and `errors.AsType` forms
([ADR 0041](../adr/0041-type-tests-on-errors.md)). To test the dynamic type on
purpose (in an `Unwrap` or `Is` method), bind the value as `any` first
(`v: any = err`); interface targets (`err is io.Reader`) do not warn.

### 5.11 Membership tests

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

## 6. Evaluation order

Lyth evaluates expressions in Go's order ([ADR 0045](../adr/0045-go-evaluation-order.md)).
Calls, method calls, and channel receives run in lexical left-to-right order,
the calls of `?` and `catch` and those in `if` and `when` values included.
Plain operands (variables, fields, elements) are read as the go compiler reads
them: after the calls of their expression. When the generator hoists part of
an expression into statements, it evaluates the operands before it that call
something into temporaries first, and leaves plain reads in place, so a `?`
never changes what the rest of the expression reads: `total + f()?` reads
`total` after `f`, as `total + g()` reads it after `g`. A pointer method called
on a field or element changes that field or element, also when an argument is
hoisted. Code should not depend on a variable that a call in the same
expression changes; Go leaves that order unspecified.

## 7. Builtins and prelude

Go's builtin functions keep their names and semantics: `len`, `cap`, `append`,
`copy`, `delete`, `make`, `new`, `panic`, `recover`, `min`, `max`, `clear`,
`close`. `print`/`println` write to standard output and lower to
`fmt.Print`/`fmt.Println`; Go's stderr builtins are not exposed. The prelude adds a
deliberately small set of methods on built-in types that lower to the standard
library. Each one replaces a common multi-line Go idiom:

| Lyth | Go | Since |
|---|---|---|
| `s.toInt()` / `s.toInt64()` / `s.toFloat64()` / `s.toBool()` (fallible) | `strconv.Atoi`/`ParseInt(s,10,64)`/`ParseFloat(s,64)`/`ParseBool` | 0.1.0 |
| `err.wrap("context")` | `fmt.Errorf("context: %w", err)` | 0.1.0 |

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
| `type H = (int) -> int` | `type H = func(int) int` |
| `type UserID int64` | `type UserID int64` |
| `struct S : I { ... }` | `type S struct { ... }` and `var _ I = S{}` |
| `func f(n: int = 1)`, called `f()` | `func f(n int)`, called `f(1)` |

### 8.3 Hygiene

Generated temporaries and renamed identifiers never capture or shadow
user-visible names. The generator keeps a scope-aware name allocator: a Lyth
local keeps its name in Go unless that would collide with a Go keyword, a Go
predeclared identifier, an import qualifier, or a package-level name used in the
same function. In those cases it gets a numeric suffix.

## 9. Tests

Test code lives in files ending in `_test.lyth`, in the package under test or,
as in Go, in an external test package (`package foo_test`, §2) that imports
it. They are
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
