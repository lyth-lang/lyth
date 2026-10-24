# Lyth Error Semantics (v0.1 draft)

This document specifies how errors are declared, propagated, handled, and
represented in Lyth. It is normative for the checker and the code generator.
The positive and negative tests listed in §11 implement it.

## 1. Model

1. **Errors are Go `error` values.** Lyth has no exception mechanism, no hidden
   error state, and no wrapper type at runtime. Lyth's `error` type *is* Go's
   `error` interface.
2. **Fallibility is part of a function's signature.** A function that can fail
   says so with `throws`. Its Go signature has a trailing `error` result, so Go
   callers see an idiomatic API and Lyth callers see a contract the checker
   enforces.
3. **Propagation is explicit at every call site.** Postfix `?` propagates and
   `catch` handles. No error passes through a call unseen, and none is dropped
   without an explicit `_ =`.
4. **Errors and absence are different.** `null` means "no value" and an `error`
   means "the operation failed". The operators never mix (`?` versus `?.`/`?:`).

## 2. Fallible results

### 2.1 Definition

A call has a **fallible result** when the callee's results end with a component
whose type is `error` (Go's predeclared `error` interface, in any nullability).
This covers:

- Lyth functions declared with `throws`;
- Go functions and methods whose last result is `error`
  (`os.Open: (string) -> (*File!, error!)`, `(*Rows).Close: () -> error!`);
- calls through function values of such types.

The other components are the **success values**. The common shapes:

| Go result | Lyth view | Value of `call?` |
|---|---|---|
| `error` | fallible, no success value | none (statement only) |
| `(T, error)` | fallible, one success value | `T` |
| `(T, U, error)` | fallible, two success values | tuple, destructured: `t, u := call?` |

Only the static type `error` counts. Concrete error types in the last position
(`func f() *MyErr`) are not fallible results, because converting them to `error`
would create Go's typed-nil trap. Such functions are called normally.

### 2.2 `throws` functions

```kotlin
func parsePort(s: string): int throws { ... }      // Go: func parsePort(s string) (int, error)
func save(u: User) throws { ... }                  // Go: func save(u User) error
func minMax(xs: []int): (int, int) throws {...} // Go: func minMax(xs []int) (int, int, error)
```

Inside a `throws` function:

- `return v` returns successfully: Go `return v, nil`.
- Falling off the end of a function with no success value returns success:
  Go `return nil`.
- `throw e` fails: Go `return <zero values>, e`. The operand must have type
  `error` and be non-null (E0305). Nullable errors must be checked first, which
  rules out "returning a nil error by accident" by construction.
- `?` is allowed (§3).
- `return f()`, where `f()` is fallible and its success values have the
  function's result types, returns the call's results as they are, error
  included: Go `return f()` (§3.6).

`throws` appears in function types too: `(string) -> int throws`.

### 2.3 Explicit result tuples ending in `error`

A Lyth function may also declare a raw tuple whose last element is `error?`. This
exists for one reason: implementing Go interfaces whose contract returns values
*together with* an error, such as `io.Reader.Read`, which returns `n > 0` along
with `io.EOF`.

```kotlin
func *Upper.Read(p: []byte): (int, error?) {
    n, err := src.Read(p)
    for i in 0..<n { p[i] = toUpper(p[i]) }
    return n, err
}
```

In such functions `return a, b` returns exactly what is written. `throw e` and `?`
are also allowed and return zero values for the other results. Use `throws` for
everything else; the checker suggests it (W0003) when a tuple-with-error
function only ever returns `zero, err` or `v, null`.

## 3. Propagation: postfix `?`

### 3.1 Rule

`e?` requires:

1. `e` is a fallible result (E0302 otherwise: "`?` needs a call that returns an
   error; `x` has type `T`"). If `e` is nullable instead, the hint points to
   `?:` and `!!`.
2. The innermost enclosing function or lambda is fallible: `throws`, a raw tuple
   ending in `error`, or a lambda whose expected type is fallible (E0301
   otherwise, with a fix-it to add `throws`).

Evaluation: evaluate `e`. If its error component is non-nil, return from the
enclosing function immediately with that error (unchanged) and zero values for
the other results. Otherwise `e?` evaluates to the success values.

The error is propagated **unchanged**: no wrapping, no stack capture, no
allocation. `errors.Is`/`errors.As` on the caller's side see exactly the
callee's error. To add context use `catch` or `wrap` (§4.3).

### 3.2 Typing of the success value

For a Lyth `throws` function, the success value has its declared type (non-null
stays non-null: success implies a real value). For Go functions, success values
have their lifted (platform) types, because Go's convention does not guarantee a
non-nil value on success (`(nil, nil)` "not found" APIs exist).

### 3.3 Chaining

`f()?` is a postfix expression and composes with member access: `f()?.name`,
`load(path)?.Validate()?`. Since `?.` is lexed as one token, `x?.y` where `x` is
a fallible result is defined as `(x?).y`. A fallible result is a tuple, never a
nullable value, so the safe-call reading would be meaningless and no ambiguity
exists. When the success value is itself nullable, write `x??.y`
(propagate, then safe call). The checker's hint suggests this when needed.

### 3.4 Errors versus null

```kotlin
func *Repo.Find(ctx: context.Context, id: int64): User? throws  // Go: (*User, error)

func handler(c: *web.Ctx) throws {
    id := c.PathValue("id").toInt64()?             // bad input -> error
    user := repo.Find(c.ctx(), id)?                // db failure -> error
        ?: return c.notFound("no user ${id}")        // missing -> normal flow
    c.json(200, user)
}
```

`?` never turns null into an error and `?:` never swallows an error. They read
left to right: first "did it fail?", then "is anything there?".

### 3.5 Evaluation order and conditional contexts

`?` may appear in any expression position. The generator hoists the call and its
check into statements, spilling earlier operands with side effects into
temporaries so that left-to-right order is preserved
([language.md §6](language.md#6-evaluation-order-v01)). In conditionally evaluated
positions, such as the right operand of `&&`/`||`/`?:`, an `if`/`when` branch,
or a `while` condition, the hoisted code is placed inside the corresponding
branch or loop iteration, never evaluated eagerly.

### 3.6 Returning a fallible call

In a `throws` function, `return f()` (or an expression body `= f()`) whose call
is fallible and whose success values have the function's result types passes
the call's results through: Go `return f()`. It
differs from `return f()?` only on failure, where `?` returns zero values with
the error and pass-through returns the callee's values unchanged. That is what
contracts like `io.Writer` require: a wrapper's `Write` must report the bytes
written before an error.

```kotlin
func *counting.Write(b: []byte): int throws = w.Write(b)   // func (c *counting) Write(b []byte) (int, error) { return c.W.Write(b) }
func Port(s: string): int throws = strconv.Atoi(s)             // func Port(s string) (int, error) { return strconv.Atoi(s) }
```

Values that would have to change on the way cannot pass through: boxing into
`T?`, and a concrete value into an interface, which would turn the nil pointer
of a failing call into a non-nil interface (Go's typed-nil trap). Interface
values and identical types pass. Elsewhere write `return f()?`, which returns
zero values on failure. Functions declaring a raw result tuple return exactly
what is written anyway (§2.3).

## 4. Handling: `catch`, `throw`, `wrap`, and explicit discard

### 4.1 `catch`

```ebnf
CatchExpr  = PostfixExpr { TypedCatch } [ "catch" [ "(" Binder ")" ] Block ] .  (at least one catch)
TypedCatch = "catch" "(" Binder ":" Type ")" Block .
Binder     = identifier | "_" .
```

`e catch (err) { handler }` requires a fallible `e`. If `e` succeeds, the
expression evaluates to its success value. Otherwise `err` is bound to the
(non-null) error and the handler block runs. The block must either produce a
value assignable to the success type through its last expression, or diverge
(`return`, `throw`, `continue`, `break`, `panic`). The expression's type is the
common type of the success type and the block's type, so `catch { null }` makes
the result nullable. For a value type the success value is boxed:
`s.toInt() catch { null }` is an `int?`.

```kotlin
port := env("PORT").toInt() catch { 8080 }                    // fallback
cfg := loadConfig(path) catch (err) {                           // log and default
    log.Warn("using default config", "err", err)
    defaultConfig()
}
user := repo.Find(ctx, id) catch (err) { throw err.wrap("find user ${id}") }  // context
```

The handler block is a plain block, not a lambda: `return` inside it returns from
the enclosing function. The binder is optional and `_` is allowed.

A call with several success values is caught when it is destructured or
assigned: `a, b := pair() catch { return }`, `a, b = pair() catch { continue }`,
and likewise `a, b = pair()?`. The handler cannot supply several values, so it
must exit (E0307). It lowers to `a, b, err := pair()` and
`if err != nil { ... }`.

**Typed handlers [v0.1].** `catch (e: T)` handles only errors of type `T`, a
non-null type that implements `error`: usually a pointer to an error struct
(`*NotFound`) or a Go error type (`*pgconn.PgError`). It matches like
`errors.AsType[T]`, so it sees through wrapping, and binds `e` as a `T`. Clauses
are tried in order. Errors no clause handles are still errors, so the
expression stays fallible and ends with `?` to propagate them or with an
untyped `catch` to handle them; leaving them unhandled is E0303.

```kotlin
user := repo.Find(id) catch (_: *NotFound) { guest }?          // others propagate
msg := lookup(key) catch (e: *NotFound) {
    return "no ${e.Key}"
} catch (e: *Invalid) {
    return "bad request: ${e.Reason}"
} catch (err) {
    return "failed: ${err}"
}
```

The chain lowers to one error check and an `if`/`else if` dispatch on
`errors.AsType`, ending in the final handler or in a return of the error:

```go
user, err := repo.Find(id)
if err != nil {
	if _, ok := errors.AsType[*NotFound](err); ok {
		user = guest
	} else {
		return User{}, err
	}
}
```

Typed handlers replace Go's `var nf *NotFound; if errors.As(err, &nf) { ... }`
([ADR 0018](../adr/0018-typed-catch.md)).

### 4.2 `throw`

`throw e` is a statement and also an expression of type `nothing`, so it works in
`?:` and `when` branches. It is allowed only in fallible functions (E0304) and
requires a non-null `error` (E0305). It has no unwinding semantics.

### 4.3 Wrapping and inspection

- `err.wrap("message")` is a prelude method that lowers to
  `fmt.Errorf("message: %w", err)`, the idiomatic Go wrap.
  `errors.Is`/`errors.As`/`errors.Unwrap` see through it.
- Sentinel errors are ordinary package-level values:
  `var ErrNotFound = errors.New("not found")`. Compare them with
  `errors.Is(err, ErrNotFound)`, as in Go.
- Custom error types are structs with a `func T.Error(): string` method, the
  same name Go's `error` interface uses.
- Typed `when` over errors (branch conditions using `errors.Is`/`errors.As`) is
  **[planned]**.

### 4.4 No silently discarded errors

A fallible call used as a bare statement is E0303 ("result of `f.Close()`
includes an error that is not handled"). The fix-its offer `?`, `catch`, and
explicit discard:

```kotlin
_ = f.Close()          // explicit discard: lowered to `_ = f.Close()`
```

Capturing the error in a variable (`err := f.Close()`) counts as handling: the
error is a value. An unused binding then gets the usual warning (W0001).

**Conventional exclusions [v0.1].** A small, fixed set of Go functions may be
called as statements without handling their error, because by Go convention
their errors are not actionable: `fmt.Print`, `fmt.Printf`, `fmt.Println`;
`fmt.Fprint*` when the writer is `os.Stdout`, `os.Stderr`, a `*bytes.Buffer`,
or a `*strings.Builder`; the `Write*` methods of `*bytes.Buffer` and
`*strings.Builder`; `hash.Hash.Write`; `io.Pipe*.CloseWithError`;
`math/rand.Read`. This is exactly errcheck's default exclusion list, the de
facto convention in Go code bases. The list is closed: user code cannot
extend it.

## 5. Raw access: destructuring

`v, err := f()` binds a fallible call's components directly, Go-style. The
error component has type `error?`. Because a failing call returns zero values,
each success value has type `Z(T)`: `T` itself when `T` is zeroable, otherwise
`T?` ([types.md §6](types.md#6-zero-values-v01)). This keeps destructuring sound for
Lyth functions that return non-null pointers. Destructuring is meant for APIs whose
values matter alongside the error (`io.Reader.Read`, `io.Copy`'s byte count),
and for porting Go code. Day-to-day code uses `?` and `catch`.

## 6. Fallible function types and lambdas

- `(A) -> R throws` lowers to `func(A) (R, error)`. `() -> unit throws` lowers to
  `func() error`.
- A lambda is **fallible** when its expected type is declared `throws` in Lyth,
  or is a Go function type whose last result is `error` (`func() error` from
  `errgroup.Group.Go`, `fs.WalkDirFunc`). Go has no `throws`, so a trailing
  `error` is how a Go callback says it can fail. In a fallible lambda, `?` and
  `throw` work, the last expression or `return v` is the success value, and
  reaching the end returns success.
- A function type written in Lyth with an `error` result is not fallible: the
  lambda returns the error as its value, as a `func` declared with that type
  does (`check: (int) -> error? = { validate(it) }`). `?` and `throw` are
  E0301 and E0304 there, with a hint to return the error or declare the type
  `throws`. A tuple ending in `error?` follows §2.3. Calls through such
  function values still have fallible results (§2.1); see
  [ADR 0027](../adr/0027-lambda-fallibility.md).
- A lambda with no expected type is fallible only if annotated (`{ x: string ->
  ... }` cannot use `?`). The error message says to annotate the target type.

This is what makes library-level concurrency and handlers concise without
language features:

```kotlin
g := errgroup.Group()
g.SetLimit(20)
for url in urls {
    g.Go { fetch(ctx, url)? }        // lambda typed func() error by Go's signature
}
g.Wait()?
```

## 7. `defer`, panic, and recover

- `defer call` where `call` is fallible is E0303. Use one of:
  - `defer f.Close()?` **[planned; E0601 today]**: if the function is
    otherwise returning successfully and `Close` fails, the function returns
    `Close`'s error instead. If it is already failing, the original error is
    kept. Lowers to the idiomatic named-result deferred closure.
  - `defer f.Close() catch (err) { log.Warn("close", "err", err) }` **[v0.1]**.
  - `defer { _ = f.Close() }` to discard explicitly.
- Panics are Go panics. `?` does not catch panics, and `throw` never panics.
  `recover()` works in deferred calls as in Go. `x!!` on null panics with
  `lyth: unexpected null (file.lyth:line:col)`, a programming error rather than an
  operational one.
- A panic inside a `catch` handler or a deferred call behaves as in Go.

## 8. Errors and generics **[v0.1]**

`func retry[T](n: int, op: () -> T throws): T throws` lowers to
`func retry[T any](n int, op func() (T, error)) (T, error)`. On `throw` and
`?`, the zero value of a type parameter is emitted as `*new(T)`. A call
infers `T` from a fallible lambda's result as from any other lambda's:
`retry(3) { load()? }`. Fallibility is not a
type parameter: there is no `throws(E)` and no generic "maybe-throwing"
abstraction. Every error is a Go `error`.

## 9. `main` and tests

- `func main() throws` lowers to a Go `main` that calls the body and on error
  prints `error: <message>` to stderr and exits with status 1.
- Test functions **[v0.1]** may be `throws`. A thrown or propagated error
  fails the test with its message, reported at the test's declaration line
  (language.md §9).

## 10. Generated code (normative examples)

| Lyth | Go |
|---|---|
| `u := repo.Find(id)?` | `u, err := repo.Find(id)`<br>`if err != nil { return nil, err }` |
| `db.Ping()?` | `if err := db.Ping(); err != nil { return err }` |
| `return u` (in `User throws`) | `return u, nil` |
| `throw ErrNotFound` | `return User{}, ErrNotFound` |
| `n := s.toInt() catch { 0 }` | `n, err := strconv.Atoi(s)`<br>`if err != nil { n = 0 }` |
| `use(load()?)` | `v, err := load()`<br>`if err != nil { return err }`<br>`use(v)` |
| `return w.Write(b)` (in `int throws`) | `return w.Write(b)` |
| `n := s.toInt() catch { null }` | `var n *int`<br>`if v, err := strconv.Atoi(s); err == nil { n = new(v) }` |
| `u := find(id) catch (_: *NotFound) { guest }?` | `u, err := find(id)`<br>`if err != nil {`<br>`if _, ok := errors.AsType[*NotFound](err); ok { u = guest } else { return User{}, err }`<br>`}` |

The error variable is named `err`, and expansions in one block share it, as Go
code reuses `err`: each assignment is checked right away, and a later `:=`
redeclares it. A catch binder named `err` is that same variable. Another name
is chosen only where `err` would mean something else: a user variable named
`err`, the binder of an enclosing handler that a nested expansion would hide,
or a binder captured by a closure, which a later assignment would change. The
generated code is what a careful Go programmer writes by hand, so performance
is identical by construction. The
benchmark suite verifies this (`bench/errors`).

## 11. Test matrix

Positive (must compile and run with the expected output) and negative (must
fail with exactly the listed code at the listed position). Files live in
`tests/run/` and `tests/check/`.

| Case | Expectation |
|---|---|
| `?` on Go `(T, error)` in a `throws` function, success and failure paths | runs; error text printed by `main throws` |
| `?` on Go `error`-only call as statement | runs |
| `?` nested in call arguments preserves evaluation order | runs; side-effect order printed |
| `?` in `&&` right operand evaluated only when needed | runs |
| `catch` with fallback value / with `throw` / with `return` | runs |
| `catch { null }` makes result nullable, boxing a value type's success value | runs |
| typed `catch` chain: wrapped errors matched, others propagated by `?` or handled by a final `catch` | runs |
| `return f()` passes partial results through with the error | runs |
| `throw` in `throws` function returns zero values | runs |
| `main throws` exit status 1 and stderr message | runs |
| destructuring `(T, error)` gives `error?` and nullable non-zeroable values | type-checks |
| fallible lambda passed to a Go `func() error` parameter | runs |
| lambda typed by a Lyth `(T) -> error?` returns the error as a value | runs |
| E0301: `?` in non-throwing function | error at the `?` |
| E0302: `?` on non-fallible expression | error at the `?` |
| E0303: fallible call as bare statement | error at the call |
| E0304: `throw` in non-throwing function | error at `throw` |
| E0305: `throw` with nullable error / non-error | error at the operand |
| E0306: `catch` on non-fallible expression | error at `catch` |
| E0307: `catch` block neither yields a value nor diverges | error at the block end |
| E0303: typed `catch` without `?` or a final `catch` | error at the expression |
| E0301/E0304: `?` or `throw` in a lambda typed by a Lyth `(T) -> error?` | error with a hint to return the error or declare `throws` |
| E0401: `?` confused with null handling (`nullable?`) | E0302 with `?:` hint |
