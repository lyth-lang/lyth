# Diagnostics

Diagnostics are a product feature. Every error points at the user's `.lyth`
source, uses Lyth's vocabulary, explains expected versus actual, and suggests a
fix when one is known. Generated names and generated line numbers never appear
in the primary message.

## 1. Model

```go
type Diagnostic struct {
    Severity Severity      // Error, Warning, Note
    Code     Code          // "E0201" — stable, documented, testable
    Message  string        // one line, lower-case, no trailing period
    Span     source.Span   // primary location (file + byte range)
    Label    string        // short text shown under the primary span's carets
    Related  []Related     // secondary spans with labels ("declared here")
    Notes    []string      // "= note: ..." explanations
    Hints    []Hint        // "= hint: ..." suggestions, optionally with a concrete edit
    Cause    *GoCause      // original Go toolchain message + generated position (verbose mode)
}
```

All stages report through `diag.Sink`, which deduplicates (same code, span, and
message), caps cascades (at most one error per expression node, and nothing
reported on `BadExpr`), and sorts diagnostics by file and position before
rendering. The parser records each statement or declaration it had to recover
from (`syntax.File.Damaged`); the checker reports nothing inside those, and no
warnings at all in a file with syntax errors, since what the parser built there
is a guess. Rejected spellings the parser understands (E0005, E0008) are not
damage: the parser reports the Lyth spelling and continues normally. The lambda argument of a call whose callee failed to check gets
parameters of unknown type, so only errors of its own are reported.

## 2. Rendering

Human format (default, colored when stderr is a TTY):

```
users.lyth:12:27: error[E0201]: type mismatch in argument id of repo.find
   |
12 |     return repo.find(ctx, id)?
   |                           ^^ expected int64, found string
   |
   = hint: convert explicitly: id.toInt64()? (parsing can fail)
```

- The first line uses the `path:line:col:` prefix, so editors, `grep`, vim
  quickfix, and VS Code problem matchers parse it unchanged.
- Columns are 1-based Unicode code points. Carets span the whole primary range.
  Tabs are expanded consistently in the snippet and the caret line.
- `-diagnostics=json` emits one JSON object per diagnostic with byte offsets,
  line/column pairs, and all fields above, for editors and tests. A
  diagnostic mapped from the Go toolchain also has a `cause` object with the
  Go `message`, its `position`, and the `mapping` kind.
- `-v` adds `Cause` blocks: the Go message, the generated file and position, and
  the mapping kind (exact, line-level, or unmapped).

## 3. Code catalog

| Range | Area | Codes |
|---|---|---|
| E00xx | Syntax | E0001 invalid character · E0002 unterminated literal · E0003 unexpected token · E0004 invalid number · E0005 `:=` declares nothing new (the name is already declared in this scope; hint: use `=`) · E0006 `$name` without braces in a string, where `name` is a variable in scope (hints `${name}` and `\$name`, no fix) · E0007 invalid escape · E0008 syntax from another language or an earlier Lyth spelling (`fun`, local `val`/`var`, `this`, `Map<K, V>`, `List<T>`, `<T>`, Go receivers, `range`, `pub`, `<-`), with the Lyth spelling, usually as an exact edit |
| E01xx | Names & packages | E0101 undefined name (an old type name such as `Int` gets the edit `int`) · E0102 duplicate declaration / Go-name collision · E0103 import cannot be loaded · E0104 not a package/type/value where one is needed · E0105 unexported declaration used from another package · E0106 unknown member of package |
| E02xx | Types | E0201 type mismatch · E0202 mismatched operand types · E0203 wrong argument count · E0204 not callable · E0205 unknown field or method · E0206 missing return · E0207 wrong number/types of return values · E0208 cannot infer type · E0209 invalid operation for type · E0210 multiple values in single-value context · E0211 invalid struct construction · E0212 does not implement interface · E0213 invalid type arguments · E0214 invalid conversion · E0215 constant overflow · E0216 invalid use of a construct (e.g. a non-exhaustive `when`, `self` outside a method) |
| E03xx | Errors | E0301 `?` outside fallible function · E0302 `?` on non-fallible expression · E0303 unhandled error result · E0304 `throw` outside fallible function · E0305 invalid `throw` operand · E0306 `catch` on non-fallible expression · E0307 `catch` block must yield a value or exit |
| E04xx | Nullability | E0401 nullable value used as non-null · E0402 `null` for non-nullable type · E0405 non-zeroable type without initializer · E0406 nullable slice or map · E0407 nullable type parameter (not supported yet) |
| E05xx | Mutability | E0501 assigning to a read-only binding (parameter, loop variable, `catch` binding, `self`, constant) · E0502 mutating through a read-only binding (a value receiver's fields) · E0503 address of a read-only binding · E0504 pointer-receiver call on a read-only value |
| E06xx | Interop | E0601 unsupported construct (with the specific reason) · E0602 Go toolchain/version problem |
| E09xx | Internal/toolchain | E0901 internal compiler error · E0902 generated Go failed type check · E0903 Go compiler error · E0904 Go linker / toolchain error |
| W0xxx | Warnings | W0001 unused local · W0002 unused import · W0003 prefer `throws` · W0004 unnecessary safe call / `!!` · W0005 unreachable code · W0006 a declaration hides a variable of an enclosing scope · W0007 a type test of an error type on an `error` value, which misses wrapped errors · W0008 an automatic json key that splits a run of capitals (`ETag` → `eTag`), or two fields with the same json key · W0009 a json key that ADR 0036 changed (`IDs` was `iDs`; a tag without a name used the field name) |

Codes never change meaning once released. `lyth explain E0301` printing long-form
explanations is **[planned]**.

## 4. Message style

- State the problem, then the facts: "type mismatch in argument id of repo.find",
  then a label "expected int64, found string".
- Use Lyth names for types (types.md §12), which are Go's spellings (`int`,
  `map[string]int`) plus Lyth's own (`User?`, platform `error!`, `untyped int`),
  and Lyth names for Lyth declarations.
  Go declarations keep their Go names, because that is what the user wrote.
- Never mention generated temporaries (`err2`), generated files, or Go-only
  concepts unless the error concerns interop, and then label them as Go
  ("Go method FindUser in package example.com/repo").
- Hints carry concrete edits where possible (insert `?`, add `throws`, wrap
  with `float64(...)`, write `func` for `fun`, use `=` instead of `:=`), so an
  LSP can offer quick fixes.
- An edit that only respells what the code already means (an earlier Lyth
  spelling, or a Go or Kotlin one) is a *fix* (`WithFix`, `"fix": true` in
  JSON), and `lyth fix` applies it unasked. An edit that changes behavior
  (adding `?`, a numeric conversion, a guessed name) is never a fix. When a
  release changes syntax, the old form keeps a diagnostic with a fix
  ([ADR 0033](../adr/0033-agents-learn-from-the-compiler.md)).

## 5. Diagnostics by class

| Class | Strategy |
|---|---|
| Syntax errors | Expected-token sets from the parser state ("expected `)` to close the call started at 3:14"). Recovery prevents cascades. Specific diagnostics for common slips, each with the Lyth spelling: Kotlin habits and earlier Lyth spellings (`fun`, `val`, `this`, `for (x in xs)`, `List<T>`, `<T>`), Go habits Lyth spells differently (receivers `func (s *S) M()`, `for i, x := range xs`, `ch <- v`, `[T C]`), a `:=` that declares nothing new, a body without braces, `"$name"`. |
| Undefined names | Nearest-match suggestions (edit distance ≤ 2) from the visible scope, and, for selectors on Go packages, exported names of that package ("did you mean `http.StatusOK`?"). |
| Invalid imports | Go loader errors at the import span, with `go get` suggestions. |
| Inference failures | Point at the expression with no type source and suggest an annotation. |
| Generic arguments | Name the type parameter and constraint, and give `go/types`' reason. |
| Invalid `?` | E0301/E0302 with "add throws to the declaration of X" or "use `?:` for nullable values". |
| Return types | Point at the `return` expression, plus a related span at the declared result type. |
| Interfaces | List the missing or mismatched methods, with each expected signature and the reason (pointer receiver, a name that differs in case, wrong nullability). |
| Overloads | Lyth has no overloading, so there is nothing to disambiguate. A duplicate name is E0102. |
| Duplicate declarations | Primary span on the second declaration, related span on the first. |
| Unsupported interop | E0601 naming the construct and the planned alternative. |
| Go compiler errors | §6. |
| Linker errors | E0904 with the Go linker text. When it mentions a symbol, the symbol is mapped back to its Lyth declaration via the name table. |

## 6. Mapping Go toolchain errors

Three sources, in order of preference:

1. **Lyth checker**: most errors, native spans.
2. **In-process `go/types` validation of generated code**: structured
   `types.Error{Pos, Msg, Soft}`. The position is read *unadjusted* (raw
   generated offset) and mapped through the exact source map to the innermost
   Lyth span covering it. The message is translated: Go type spellings are
   rewritten to Lyth spellings token-wise using the types involved, and
   generated temporaries are replaced by the Lyth expression they stand for (the
   span table knows). Reported as E0902 with the original message preserved as
   `Cause`.
3. **`go build -json`**: build events with structured package boundaries
   (`ImportPath`, `Action`, `Output`). Lines inside `Output` are compiler
   diagnostics `file:line[:col]: message`. They are parsed with a position parser
   that accepts only the known generated and Lyth file paths, never by
   regex-guessing arbitrary text. Positions already point at `.lyth` lines thanks
   to `//line`. Lines that don't match are kept verbatim as notes, never dropped.
   Non-JSON stderr (early fatal errors) is attached as a cause.

**Fallback when exact mapping is impossible:** use line-level mapping (the
`//line` file and line, column unknown). If there is no mapping at all (an error
in a synthesized package-level helper, or a linker error), attach the
diagnostic to the nearest enclosing Lyth declaration, or to the package, and
mark it "location approximate", with the full Go output in `Cause`. A Go
diagnostic is never shown to the user without its original text available.

## 7. Testing diagnostics

- `tests/testdata/check/*.lyth` contain inline expectations:
  ```kotlin
  x: int = "a"   // ERROR E0201 "expected int, found string"
  ```
  The harness compares code, line, and a message substring. Every expected
  diagnostic must occur, and no unexpected error may occur; warnings are
  checked in files with a `// WARN` expectation or a `// Warnings: none` line.
- Toolchain-mapping tests deliberately produce `go/types` validation errors and
  `go build` errors (e.g. via unsupported constructs or a linker failure) and
  assert the mapped positions.
- A fuzz test feeds random token streams to the parser and checker and asserts
  that no panic escapes (internal errors must be diagnostics).
