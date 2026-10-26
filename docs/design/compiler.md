# Compiler Architecture

## 1. Shape of the pipeline

```
 .lyth files ──► load ──► lex ──► parse ──► resolve ──► check ──► lower ──► emit ──► validate ──► go build
                  │                 │          │           │          │         │          │            │
                  │               AST+spans    │      typed info   go/ast +   Go text +  go/types     binary
                  │                            │     (side tables)  span tbl  source map  errors       + mapped
                  └── module/package graph     └── Go packages via go/packages (export data)            diagnostics
```

| # | Stage | Package | Input → Output |
|---|---|---|---|
| 1 | Source loading, module resolution | `internal/build`, `internal/source` | dirs → `source.File`s grouped into packages, module root from `go.mod` |
| 2 | Lexing | `internal/syntax` | bytes → tokens (with `NEWLINE` and template tokens) |
| 3 | Parsing | `internal/syntax` | tokens → `ast.File` (every node has a `source.Span`) |
| 4 | Name resolution | `internal/check` | package scopes, imports, symbols |
| 5 | Type checking | `internal/check` | `check.Info` side tables: types, symbols, conversions, call kinds |
| 6 | Semantic validation | `internal/check` | flow checks: returns, smart casts, mutability, unhandled errors |
| 7 | Lowering | `internal/lower` | typed AST → `go/ast` + span table (node → Lyth span) |
| 8 | Emission | `internal/emit` | `go/ast` → gofmt-formatted text + exact source map + `//line` directives |
| 9 | Validation + toolchain | `internal/build` | in-process `go/types` check of generated code, then `go build/run/test/vet -overlay -json` |
| 10 | Diagnostic mapping | `internal/diag`, `internal/build` | Go positions → Lyth spans; Go messages → Lyth terminology |

Stages communicate only through these data structures. Parsing knows nothing
about types, lowering knows nothing about the CLI, and diagnostics flow through
one `diag.Sink`. Every stage is deterministic: no map iteration order, time, or
environment leaks into output.

## 2. Implementation language and dependencies

The compiler is written in **Go** (ADR 0002):

- First-class access to `go/types`, `go/ast`, `go/printer`, `go/constant`,
  `go/build`, the same libraries the Go toolchain and gopls are built on.
- One static binary, cross-platform, installable with `go install`.
- The audience already has a Go toolchain.

External dependency: `golang.org/x/tools` (for `go/packages`) only. It is the
officially maintained package loader and covers modules, workspaces, vendoring,
overlays, build tags, and cgo, which would be costly and risky to replicate. The
lexer, parser, checker, and diagnostics renderer are hand-written: parser
generators give poor error recovery and poor diagnostics, and both are product
features here.

## 3. Front end

### 3.1 Sources and positions

`source.File` holds the path, content, and line-start table. A `source.Pos` is
`(file index, byte offset)` and a `source.Span` is a half-open byte range. Line
and column (1-based, columns in Unicode code points) are computed only for
display. All stages track spans, never line/column pairs.

### 3.2 Lexer

Hand-written and rune-based. Notable behavior:

- emits `NEWLINE` outside `()`/`[]` (the parser decides whether it is a
  terminator, language.md §1.3);
- string templates are lexed with a mode stack: `"a ${x} b"` →
  `STRING_START "a " TEMPLATE_START <tokens for x> TEMPLATE_END " b" STRING_END`.
  Brace depth is tracked so `${ m[{...}] }` works. A plain string is one `STRING`
  token;
- lexes `:=` as the declaration token (`x := e`, `a, b := f()`);
- recognizes Go's keywords where Lyth uses them (`func`, `map`, `chan`, `const`,
  `var`) and Lyth's own (`when`, `throws`, `self`, ...). `val`, `pub`, and
  `private` stay keywords only so the parser can reject them with the Lyth
  spelling (E0008); `fun` and `this` are ordinary identifiers, reported where
  the old syntax would have used them;
- never fails hard: bad characters become `ILLEGAL` tokens with a diagnostic, so
  parsing continues.

### 3.3 Parser and AST

Recursive descent for declarations and statements, Pratt parsing for
expressions with the precedence table in language.md §5.1. Error recovery
synchronizes at `NEWLINE`/`}` for statements and at declaration keywords for
top-level items, so one file reports multiple syntax errors. A node that could
not be parsed becomes `BadExpr`/`BadStmt` with a span, so later stages can skip
it without cascading errors.

Headers are not parenthesized (`if c {`, `while c {`, `for x in xs {`,
`when x {`), so the `{` after a header expression always starts the body: inside
a header the parser takes no trailing lambda (Go's rule for composite literals
in headers), and `if (xs.Any { it > 0 }) {` needs the parentheses. A body that
is not a block is reported with the edit that adds braces.

Type arguments use brackets, and the parser does not decide whether `x[A]` is
an index or an instantiation. It reads `x[A]` as an index expression whose
operand may be a type: a type-only form (`[]T`, `map[K]V`, `chan T`) is parsed
as a type, and `x[A, B]` can only be an instantiation. The checker decides
`x[A]` as `go/types` does: when `x` names a generic function or type, it is an
instantiation and the operand is read as a type; otherwise it is an index. No
speculative parse is needed for valid code. The only lookahead parse left is a
bounded one that recognizes the old `f<T>(...)` spelling, so it can be
reported (E0008) with the bracket form as the edit.

Old spellings the parser understands (`fun`, local `val`/`var`, Go receivers
`func (s *S) M()`, `for (x in xs)`, `for i, x := range xs`, `Map<K, V>`,
`<T>`, `typealias`, `type X struct { ... }`, `import "x" as y`, Go's
`Name Type` fields) are reported with their replacement, and parsing continues normally
after them: they do not count as damage (diagnostics.md §1).

The AST is a syntax tree only: no types, no resolved symbols. Those live in
side tables keyed by node pointer. The AST stays immutable and reusable (for
an LSP or formatter).

## 4. Middle end: resolution and checking

### 4.1 Packages and symbols

`check.Package` holds the package scope, the Lyth symbols (`FuncSym`, `TypeSym`,
`VarSym`, `ConstSym`), and a **shadow `go/types.Package`** with lowered
declarations (types.md §1). The shadow package lets Lyth ask `go/types`
Go-level questions (method sets, `types.Implements`, `types.AssignableTo`,
`types.ConvertibleTo`, `types.Instantiate`, comparability) about Lyth types too.

Go packages arrive as `*types.Package` from `internal/golang` (go/packages).
Lyth packages in the same build are checked in dependency order and imported
from their `check.Package`.

### 4.2 Checking order

1. Collect top-level declarations of all files and report duplicates.
2. Resolve imports.
3. Resolve type declarations (two passes, so recursive types work), then struct
   fields and interface methods, then function signatures. Build shadow objects.
   Declaration order within a package does not matter, so three things wait
   for the declarations they depend on:
   - A type parameter's constraint that names a Lyth type of the package
     (`struct Cache[S: Store[int]]`, `struct Box[S: Plain]`) is given to
     `go/types` only after every type has its definition; `go/types` cannot
     compute the type set of an interface whose definition is not set.
   - Interfaces are completed (their method sets computed) after all are
     defined, since one may embed an interface declared later. Embedding
     cycles are reported (E0216) and removed first.
   - An instance written in a declaration (a field `Box[mem]`, a parameter
     `Cache[T]`, an alias) is checked against its constraints only after all
     methods are declared, because `mem`'s methods may come later in the
     file. Instances in bodies are checked as they are met.
4. Check top-level initializers, then function bodies (each independent, so
   parallelizable later).
5. Flow analyses per body: definite return, smart-cast narrowing, mutability,
   unhandled errors, unused locals, shadowing (W0006). Smart casts need a value
   that cannot change between the check and the use: parameters, `self`, and
   locals the function never reassigns. Before a body is checked, a syntactic
   pass collects every name that is the target of `=`, a compound assignment,
   `++`/`--`, or `&`, or that a later `:=` list redeclares, anywhere in the
   body including its lambdas (`stable.go`). It goes by name, so it is
   conservative: reassigning one `x` unsettles every `x` in that function.
   Package-level variables never smart-cast.

### 4.3 Recorded information (`check.Info`)

| Table | Content |
|---|---|
| `Types[expr]` | Lyth type, plus constant value for constant expressions |
| `Defs[ident]`, `Uses[ident]` | resolved symbol: local, parameter, receiver member, package-level Lyth symbol, Go object, package name, builtin |
| `Calls[call]` | kind (function, method, construction, conversion, builtin, prelude), signature, argument-to-parameter mapping |
| `Selections[sel]` | field or method, Go or Lyth, implicit address/deref, safe call, error propagation |
| `Implicit[expr]` | conversions inserted: box `T→T?`, unbox (smart cast of an optional value), interface conversion |
| `Fallible[node]` | for `?`/`catch`/`throw`: the success types and the enclosing function's result shape |

The lowering stage reads only `Info`. It never re-derives types.

## 5. Back end: lowering and emission

### 5.1 Why `go/ast` is the intermediate representation (ADR 0001)

The output language is Go. Its standard AST is a complete, maintained, and
well-tested IR with a canonical printer (`go/printer`, which *is* gofmt) and a
type checker (`go/types`). A custom IR between the typed Lyth AST and `go/ast`
would mostly duplicate `go/ast`'s statement and expression forms. So lowering
targets `go/ast` directly, and each emitted node is recorded in a **span table**
(`map[ast.Node]source.Span` plus a "synthetic" flag for compiler-introduced
code). If optimization passes ever need a Lyth-specific IR, it can be inserted
between `check` and `lower` without touching the front end or emission.

Lowering never produces Go text by string concatenation. Literals go through
`strconv.Quote` and identifiers through the name allocator, so injecting code
through crafted source is impossible.

### 5.2 Lowering techniques

- **Expression linearization.** Each Lyth expression lowers to `(prelude
  statements, Go expression)`. Constructs that need statements (`?`, `catch`,
  `if`/`when` expressions, `?.`/`?:` chains, `!!`) append to the prelude. When a
  later operand needs a prelude, earlier operands that call something are
  spilled into temporaries to keep the calls in lexical order; plain reads stay
  where Go reads them, after the calls ([ADR 0045](../adr/0045-go-evaluation-order.md)).
  A receiver whose pointer method takes its address is spilled as that
  address. Short-circuit contexts (`&&`,
  `||`, `?:`, branches, loop conditions) get their own nested preludes, placed
  in the corresponding Go branch or loop. Expressions without preludes are
  emitted as written, so the common case stays verbatim.
- **Statement forms are preferred over helpers.** `x := if c { a } else { b }`
  becomes `var x T; if c { x = a } else { x = b }`. There are no immediately
  invoked closures or runtime helper calls. A returned `if`/`when` returns from
  each branch instead, and an `else` after a returning branch is outdented
  (Go's early-return style) when that moves no declaration into the enclosing
  scope.
- **`when` forms.** All-`is` conditions become a type switch; value conditions
  a Go `switch`; anything else (mixed tests, a condition that needs statements,
  which a `case` cannot hold without evaluating it eagerly) an `if`/`else`
  chain, so every condition runs only when reached. A Lyth `break` inside a
  lowered `switch` targets the loop: the loop gets a label (`loop:` …
  `break loop`) only when such a break exists.
- **Cast bindings.** A smart cast by a type test reads `x.(T)`, an assertion
  that cannot fail. Where a test opens a region in which the cast holds, the
  value is bound once instead (`if nf, ok := err.(*NotFound); ok`,
  `switch s := s.(type)`, `nf, ok := err.(*NotFound); if !ok { return }`), and
  reads in the region use the binding. Bindings nobody reads become `_`, and
  names whose Go scope has ended (an `if` init's `ok`) are released for reuse.
- **Result shapes.** For `throws` functions the lowering knows the zero value
  expression of each result type (`""`, `0`, `nil`, `T{}`, `*new(T)` for type
  parameters) for `throw` and `?`.
- **Hygiene.** A scope-aware name allocator (language.md §8.3) assigns Go names:
  user names are kept when possible, temporaries get descriptive bases (`err`,
  the callee name), and every function body is pre-scanned for the package-level
  names and import qualifiers it uses, so locals never shadow them.
- **Imports** are added on demand and aliased only on conflicts.

### 5.3 Emission and source maps (ADR 0005)

1. Print the `go/ast` file with the gofmt configuration (`printer.UseSpaces |
   printer.TabIndent`, tab width 8).
2. Reparse the printed text with `go/parser`, walk both trees in preorder in
   lockstep (ignoring `ParenExpr`, which the printer inserts for precedence),
   and record for every emitted node with a span: *generated byte range → Lyth
   span*. Any structural mismatch is an internal compiler error, which makes this
   step a free consistency check on lowering.
3. Insert `//line file.lyth:N` directives (no column). Build output (the
   files in `.lyth/gen`) gets one wherever the Go line numbering would
   otherwise differ from the Lyth line of the first mapped node on a line,
   compiler glue included, so debuggers, profilers, and vet see the exact
   Lyth line of every statement (asserted for every test program). Output for
   reading (`lyth gen -stdout` and `-in-tree`) is sparser: it has directives
   only where the numbering would be wrong for **user code that can fail at
   run time**, meaning lines whose first statement contains a call, an index,
   a dereference, a field selection, a division, a conversion, or a channel
   operation. There, compiler glue (`if err != nil {`, `return zero, err`,
   guarded unboxing, temporaries) and statements that cannot fail
   (`size = "large"`) get no directive of their own, and their numbering may
   drift. Directives start at column 1. `go/printer` preserves this, so the
   output is gofmt-idempotent (asserted for every test program).
4. Write the file plus a JSON sidecar source map (`*.lyth.go.map`) for external
   tools.

Between steps 1 and 2, blank lines directly after `{` or before `}` are
removed (outside multi-line string literals). `go/printer` keeps a blank line
wherever positions leave a gap, and lowering creates such gaps: the first branch
of a returned `when` lies one line below the `return`. Idiomatic Go has none
there, and removing them before the reparse keeps the map exact.

Two maps serve two consumers:

| Consumer | Mechanism | Precision |
|---|---|---|
| Go compiler errors, `go vet`, panics and stack traces, delve, pprof | `//line` directives | Lyth file + line |
| Lyth's own validation (`go/types` on generated code), coverage profiles, tooling, verbose diagnostics | exact source map | Lyth span (line + column range) |

The cover tool records physical positions in the generated file and takes only
the file name from `//line` directives, so `lyth test` rewrites the profile
after `go test` exits (`internal/build/cover.go`): each block becomes the
range of the Lyth code its outermost nodes came from, blocks that land on the
same range (the error check and the return of a postfix `?`) merge and count
as covered only if all of them ran, and nested ranges are split so that every
byte belongs to its innermost block, because profiles describe disjoint
ranges. Lyth files are named by absolute path: `go tool cover` resolves an
import-path name with `go list`, which finds no package in a directory of Lyth
files. `go tool cover -func` parses every file as Go, so `lyth cover -func`
computes per-function coverage from Lyth declarations.

Directives carry no column on purpose. A `//line f:L:C` directive assigns column
`C` to the first byte of the next generated line (indentation included) and
offsets everything after it. Since generated text differs from the source,
those columns would be wrong. Without a column the Go compiler reports
`file.lyth:12:` honestly, and the exact columns come from step 2.

Paths in directives are absolute in overlay builds. Stack traces then match
regular Go behavior, and `-trimpath` rewrites them to `module/path/file.lyth`
(verified). `lyth gen -in-tree` uses bare file names for reproducible committed
output.

## 6. Build orchestration and the Go toolchain

### 6.1 Generated artifacts (ADR 0006)

```
<module root>/
  go.mod                      # the user's; never modified by lyth build
  users/users.lyth
  .lyth/
    gen/users/users.lyth.go    # generated Go
    gen/users/users.lyth.go.map
    overlay.json              # {"Replace": {"<root>/users/users.lyth.go": ".lyth/gen/users/users.lyth.go"}}
```

`lyth build/run/test/vet` pass `-overlay=.lyth/overlay.json` to the `go` command,
so Go sees each generated file *inside its package directory* without anything
being written to the source tree. The `go` tool ignores directories starting
with `.`, so `.lyth/` never becomes a package. The user's files and `go.mod`
are never rewritten.

The exception is `lyth test` with coverage flags. `go test -cover` hands each
non-test file of an instrumented package to the cover tool by its path, and
the cover tool reads it from disk: a file that exists only in the overlay is
"no such file", and an overlaid file that also exists on disk is instrumented
in its disk version (reproduced with plain Go, 1.26 and 1.27). For those runs
`lyth test` writes the generated non-test files beside their sources and
removes them when `go test` exits, also after an interrupt; a file left by
`lyth gen -in-tree` is restored, and one that Lyth did not generate stops the
run.

`lyth gen -in-tree` writes the same files into the source tree (next to the
`.lyth` files) for inspection, committing, or building with plain `go build`.
Its `//line` directives use bare file names, so committed output is
reproducible across machines. Publishing a Lyth library to the Go ecosystem
requires this, because the module proxy serves only committed files.

**Dependencies.** Plain `go mod tidy` cannot see imports that exist only in
`.lyth` files, and removes their requirements. `lyth mod tidy` writes an
overlay of import stubs (one Go file per `.lyth` file, `_test.lyth` files
included so test-only modules stay, with a blank import per import path, built
by parsing alone so it works while dependencies are still missing) and runs
`go mod tidy -overlay=...`. `go get` needs no wrapper.

**Single files outside a module.** `lyth run hello.lyth` synthesizes a module in
the user cache directory (`<UserCacheDir>/lyth/run/<hash of the path>`) so
repeated runs reuse Go's build cache. The `.lyth` file is never copied, and
`//line` directives point at its real path.

### 6.2 Generated file names

| Lyth | Go | Why |
|---|---|---|
| `users.lyth` | `users.lyth.go` | |
| `users_test.lyth` | `users.lyth_test.go` | Go detects tests by the `_test.go` suffix |
| `net_linux.lyth` | `net_linux.lyth.go` | `go/build` reads GOOS/GOARCH tags from the part before the first dot |
| `net_linux_test.lyth` | `net_linux.lyth_test.go` | both rules |

These rules were verified against `go list` (see the toolchain tests).

### 6.3 Toolchain relationship

| Concern | Behavior |
|---|---|
| `go.mod` / `go.sum` | The user's module owns dependencies. `lyth build`, `run`, and `test` never edit `go.mod` (`lyth init` and `lyth mod tidy` do); a missing dependency gets a diagnostic with the exact `go get` command. |
| `go build` / `go run` | Invoked with `-overlay` and `-json` (Go 1.24+ structured build output). Flags after `--` are passed through. |
| `go test` | `lyth test` compiles the packages with their `_test.lyth` files (only the packages under test, like `go test`) and runs `go test -overlay` with go test's own flags, from the invoking directory. Hand-written `_test.go` files may sit beside `.lyth` files. Test functions keep their Go test names; a throwing test gets a wrapper that calls `t.Fatal(err)` (ADR 0014). Coverage flags write the generated files into the source tree for the run and rewrite the profile to Lyth positions (§5.3, §6.1). |
| `go vet` | `lyth vet` runs `go vet -overlay`; positions come back through `//line`. |
| `gofmt` | Generated code is gofmt-canonical by construction (tested). `lyth fmt` for Lyth sources is **[planned]**. |
| Build tags, `GOOS`/`GOARCH`, `CGO_ENABLED`, `GOFLAGS` | Inherited from the environment and passed to both `go list` and `go build`. |
| Race detector, profiling | `lyth build -- -race`. pprof and race reports show Lyth file:line through `//line`. |
| Debugging | delve resolves breakpoints and frames through DWARF line tables, which honor `//line`. Variables appear with their Go names, which are the Lyth names for locals. |
| Module cache | Shared with Go. Lyth adds no cache of its own for dependencies. |

### 6.4 Minimum Go version

Generated code targets **Go 1.26** (it uses `new(expr)`, range-over-int, and
1.22 loop semantics). `lyth` checks `go env GOVERSION` and the module's `go`
directive and reports a clear error if either is older.

## 7. Validation stage (ADR 0004)

After emission and before invoking `go build`, Lyth type-checks each generated
package in-process with `go/types`, using the same `*types.Package` objects for
dependencies (Go packages from `go/packages`, Lyth packages from their own
validation pass, in dependency order). Errors are mapped with the exact source
map to Lyth spans and reported as E0902 "generated Go does not type-check", with
the Go message as the cause. Then:

- users get a precise location even for constructs the Lyth checker does not
  fully understand yet, and
- Lyth checker bugs become visible as E0902 instead of confusing `go build`
  output.

`go build` errors after successful validation (linker errors, cgo, toolchain
problems) are parsed from `-json` build events: E0903 for compile errors at a
mapped position, E0904 for link errors without one.

## 8. Caching and incremental compilation

Lyth regenerates every Lyth package in the module on each build. Front-end and
codegen cost is milliseconds per file, and the Go build cache makes the
`go build` step incremental.

**Persistent Go type cache (implemented).** Loading Go dependencies through
`go list -export` costs 80–220 ms per build, more than everything else Lyth does.
`internal/golang/cache.go` records, per set of requested import paths, the
export-data files `go list` returned (content-addressed files in the Go build
cache) and reads them back with `gcexportdata` on later builds. The key covers
everything that can change the answer: `go env` (GOVERSION, GOOS, GOARCH,
GOFLAGS, CGO_ENABLED, GOEXPERIMENT, GOROOT, GOWORK; the same `go env -json` call
serves the toolchain check), build tags, `go.mod`/`go.sum`/`go.work`/
`go.work.sum`/`vendor/modules.txt`, and the requested paths. Local packages in
the dependency closure (the main module's Go packages and local `replace`
directories) can change without any of that changing, so each entry also stores
a fingerprint (names, sizes, mtimes) of their directories. A missing export file
(after `go clean -cache`) or any mismatch falls back to `go list`. Only clean
loads are cached; the file is `.lyth/cache/gotypes.json` (8 most recent entries,
atomic rename). `LYTH_NO_CACHE=1` disables it. Effect on the benchmark module: a
warm rebuild after a code change drops from ~246 ms to ~160 ms; a build with
nothing to recompile takes ~74 ms, ~57 ms of it `go build`
(docs/design/benchmarks.md).

Planned, in order of payoff:

1. **Skip unchanged emission.** Content-hash each generated file and leave
   `.lyth/gen` files untouched when equal (preserves mtimes; helps `go build`).
2. **Package-level incremental checking.** Key = hash(Lyth sources, compiler
   version, dependency API hashes, Go dependency export-data hashes). A
   package whose key is unchanged reuses its generated output and an API
   summary of its exported declarations.
3. **Watch mode, and an LSP that reuses in-memory state.** The language server (`lyth lsp`,
   `internal/lsp`) recompiles the edited package on every change today.

## 9. Determinism and reproducibility

- Sorted iteration everywhere (declarations in source order, imports sorted by
  `go/printer`, maps converted to sorted slices).
- Generated output depends only on Lyth sources and compiler version, plus
  absolute paths in overlay-mode `//line` directives, which follow Go's own
  path behavior and are normalized by `-trimpath`.
- Header: `// Code generated by lyth from <file>.lyth. DO NOT EDIT.`, which matches Go's
  generated-file convention, so linters and gopls skip generated files.
- Golden tests pin the output byte-for-byte.

## 10. Error handling inside the compiler

Invalid user code never panics the compiler. Each stage reports through
`diag.Sink` and returns partial results. A recovered panic in any stage becomes
E0901 "internal compiler error" with the stage name, the Lyth span being
processed, and a request to report it.
