# Go Interoperability

Interop is the platform Lyth stands on. This document explains how Lyth learns
about Go packages, how Go declarations appear in Lyth, what explicit syntax
exists for Go-specific features, and what does not work yet. Every claim in §10
is backed by a test in `tests/interop`.

## 1. Requirements

1. Any exported declaration of any Go package that builds with the user's
   toolchain is usable from Lyth without wrappers or API changes.
2. Lyth never presents a simplified model of a Go type. If Lyth can't represent
   something faithfully, it says so with a diagnostic (E0601) rather than
   approximating.
3. Go code can import Lyth packages and sees idiomatic Go APIs.
4. Interop works with modules, `go.sum` verification, vendoring, workspaces,
   build tags, `GOOS`/`GOARCH`, and `GOFLAGS`, with no Lyth-specific
   reimplementation of those features.

## 2. Evaluation of approaches

The core question: how does Lyth's checker learn the types of imported Go packages?

| Approach | Correctness | Build speed | Portability | Maintenance | Modules |
|---|---|---|---|---|---|
| A. Generate Go source only; let `go build` find type errors | Poor diagnostics: errors arrive after codegen, in Go terms; no inference possible | fast | good | low | good |
| B. Hand-written Lyth declaration files for Go packages (like `.d.ts`) | Drifts from reality, every package needs bindings | fast | good | very high | poor |
| C. Parse and type-check imported Go source ourselves | Faithful, but must replicate build tags, cgo, generated files | slow (all deps from source) | fragile | high | must reimplement resolution |
| D. `go list -export` + `go/importer` reading export data | Faithful (the compiler's own data) | fast once cached | good | moderate: locate files, manage processes | via `go list` |
| **E. `golang.org/x/tools/go/packages` (export data mode)** | Faithful, identical to D | fast, ~0.4 s for `net/http`+`encoding/json`+`database/sql` warm (measured) | good | low: maintained by the Go team, used by gopls/vet/staticcheck | complete: modules, vendor, workspaces, overlays, build tags, cgo |
| F. Temporary helper Go packages | Needed only for constructs we can't express; adds build steps | slower | ok | moderate | ok |

**Decision: E.** `go/packages` invokes the real `go list`, which already
implements module resolution, build constraints, cgo preprocessing, and export
data generation (and fills the build cache the subsequent `go build` reuses).
Types come from the compiler's own export data, so they are by definition what
the compiler will check against. A is kept as a *second* layer (in-process
`go/types` validation of the generated code, ADR 0004), not as the primary
checker. F is reserved for future escape hatches. B and C are rejected.

Version skew: export data is read by the `x/tools` version linked into `lyth`.
`x/tools` supports the two most recent Go releases. `lyth` checks the toolchain
version (`go env GOVERSION`) at startup and reports an actionable error when it
is outside the supported range. A source-importer fallback (approach C via
`go/packages` with `NeedSyntax`) is the planned mitigation.

## 3. Loading

- Lyth collects the import paths of all Lyth packages being built, separates Lyth
  packages (directories containing `.lyth` files inside the main module) from Go
  packages, and loads the Go packages in one `packages.Load` call with
  `NeedName|NeedTypes|NeedImports|NeedDeps|NeedTypesSizes|NeedExportFile|NeedModule`.
- The working directory is the module root, so `go.mod`, `go.work`, `vendor/`,
  and `GOFLAGS` apply unchanged. Lyth passes through `-tags`, `GOOS`, and `GOARCH`.
- Package errors become diagnostics at the import declaration's span (E0103),
  with the Go message preserved as the cause. Example: "no required module
  provides package github.com/x/y; to add it: go get github.com/x/y".
- Results are memoized per build, and a persistent cache of export-data
  locations skips `go list` on warm builds (compiler.md §8).

## 4. How Go declarations appear in Lyth

### 4.1 Names

Go identifiers are used **verbatim**: `http.ListenAndServe`, `r.URL.Query().Get("q")`,
`user.ID`, `time.Second`. No renaming, case-folding, or getter-to-property
synthesis. Reasons (ADR 0010): one spelling per entity, copy-paste from Go docs
works, no heuristics that break on `URL`/`IPv4`/`MD5Sum`, and no collisions.

### 4.2 Types

`lift` maps `go/types` types to Lyth types (types.md §1):

| Go | Lyth |
|---|---|
| basic types | `int`, `int64`, `string`, ... (identity) |
| `error`, `any` | `error!`, `any!` |
| named types `pkg.T` | `pkg.T` (same object; type arguments lifted) |
| `*T`, `chan T`, `func(...)`, interfaces | platform (`!`) versions |
| `[]T`, `map[K]V`, `[N]T` | `[]lift T`, `map[...]...`, `[N]...` |
| `func(A) (R, error)` | `(A) -> (R, error!)`, a fallible result |
| type aliases | resolved to their target (`types.Unalias`); the alias name is kept for display |
| `unsafe.Pointer` | `unsafe.Pointer` (only with `import "unsafe"`) |

### 4.3 Objects

- **Functions and methods** are called directly. Method calls on values and
  pointers follow Go's auto-address/auto-deref rules, plus Lyth's mutability rules.
  Promoted methods and fields of embedded structs are found by
  `types.LookupFieldOrMethod`, so promotion is exactly Go's.
- **Fields** are read and written with `.`. Writes require a mutable path.
- **Constants** keep their exact constant values (`go/constant`), so untyped Go
  constants behave as untyped in Lyth (`time.Second * 5`).
- **Package-level variables** (`os.Args`, `http.DefaultClient`) are mutable paths,
  as in Go.
- **Types** can be constructed (`&http.Server(Addr = ":8080")`), converted
  (`time.Duration(n)`), embedded in Lyth interfaces, and used as type arguments.

## 5. Explicit interop syntax for Go-specific features

| Go feature | Lyth form | Notes |
|---|---|---|
| Multiple results | `a, b := f()` | Tuples are not first-class, as in Go. |
| `(T, error)` | `f()?`, `f() catch (e) {...}`, or `v, err := f()` | errors.md |
| `(T, bool)` comma-ok | `v, ok := m[k]` / `os.LookupEnv(k)` | Not converted to `T?`: that would silently change the API's shape. |
| Pointers | `*T`, `&x`, `*p`, auto-deref on `.` | `&x` works on locals, not on read-only parameters or loop variables (E0503). |
| Interfaces | used directly. Lyth types implement Go interfaces structurally by declaring methods with the Go method names (`func *H.ServeHTTP(...)`). | |
| Generic Go APIs | `slices.Contains(xs, x)`, `atomic.Pointer[Config]()` | Inference by unification; constraints checked by `go/types`. |
| Struct tags | raw-string literal after a field: ``ID: int64 `db:"id"` `` | Emitted verbatim (plus the automatic `json` key, ADR 0013). |
| Reflection-sensitive code | Capitalized Lyth fields are Go-exported under the same name, and tags are explicit or automatic (`json`) | `encoding/json`, `database/sql` scanning, and `text/template` work on Lyth structs. |
| `context.Context` | ordinary interface value: `ctx: context.Context`; `context.WithTimeout(ctx, 5 * time.Second)` | Lyth adds no implicit context. |
| Channels | `chan T`, `make(chan T, n)`, `for v in ch`, `ch.send(v)`, `ch.receive()`, `ch.close()`, `select` | Methods lower to `<-` and `close` (ADR 0024). |
| Go function values | Lyth function types and lambdas *are* Go funcs; fallible lambdas fit `func(...) (..., error)` parameters | |
| Package vars/consts | `pkg.Name` | |
| Build tags | `//go:build` lines at the top of a `.lyth` file are copied verbatim; `_linux.lyth` file suffixes work | Validated by `go/build`. |
| cgo | not inside `.lyth` files; put cgo in a Go file of a separate package and import it | Mixed packages are planned (§8). |
| Variadic | `fmt.Println(a, b)`; spread `append(xs, ys...)` | |
| Labels / goto / `fallthrough` | labeled loops (`outer@ for x in xs`, `break@outer`); no `goto` or `fallthrough` | Lyth's control flow is structured; `when` branches never fall through. |
| `iota` | Lyth enums lower to `iota` constants named `<Type><Entry>` with a `String` method (ADR 0025) | |
| Closed sets of types | `sealed interface` lowers to an interface with an unexported marker method, so Go code outside the package cannot add implementations either | |

## 6. Lyth packages seen from Go

A Lyth package compiles to a normal Go package:

- Capitalized declarations are exported and keep their names; everything else
  is unexported (ADR 0022).
- Signatures are idiomatic: `T throws` → `(T, error)`, `T?` → `*T`.
- Doc comments are carried over, so `go doc` works.
- Lyth-specific information (nullability, `throws` versus raw tuples) is not
  visible to Go. When Lyth code imports another Lyth package, it type-checks that
  package **from its `.lyth` sources**, so no metadata format is needed. Published
  Lyth modules therefore ship both the `.lyth` sources and the generated `.go`
  files (see compiler.md §6.4): Go users consume the `.go`, Lyth users the `.lyth`.

## 7. Platform specifics, cgo, and the toolchain

- `GOOS`/`GOARCH` and build tags are honored twice: by `go list` when loading Go
  packages, and by `go/build` file matching for `.lyth` files (via the generated
  file names and copied `//go:build` lines).
- cgo-dependent Go packages are imported like any other. Their exported
  declarations come from export data. `CGO_ENABLED` and the C toolchain are
  inherited from the environment.
- Cross-compilation: `GOOS=linux GOARCH=arm64 lyth build` passes the environment
  through. Nothing in Lyth is host-specific.

## 8. Known limitations (v0.1) and plans

| Limitation | Diagnostic today | Plan |
|---|---|---|
| Go and Lyth files in the same package | E0601 "package mixes Go and Lyth files" (hand-written `_test.go` files are allowed: only go test compiles them) | Two-phase check: emit Go stubs for Lyth declarations, type-check Go files plus stubs with `go/types`, then check Lyth bodies (like Kotlin/Java joint compilation stubs). |
| Embedding in Lyth structs | E0601 | Lowering to Go embedding; promotion via the shadow `go/types` package. |
| Go APIs whose last result is a concrete error type | treated as an ordinary value | Opt-in `?` with nil check before interface conversion. |
| Calling methods through `unsafe` or with `//go:linkname` | unsupported | not planned |
| Go generic methods (Go does not have them) | n/a | n/a |
| Lyth-name ↔ Go-name collisions in a package | E0102 | — |

## 9. Testing interop

`tests/interop` builds and runs Lyth programs against:

- **Standard library**: `fmt`, `strings`, `strconv`, `errors`, `os`, `time`,
  `context`, `encoding/json`, `net/http` (+ `httptest`), `database/sql`, `sync`,
  `slices`, `maps`, `sort`, `io`, `bytes`.
- **Third-party**: `github.com/google/uuid` (named array type, methods, `(T,
  error)` parsing), later `github.com/go-chi/chi/v5` and `github.com/jackc/pgx/v5`.

Each test covers a construct: struct construction, field access, methods (value
and pointer receivers), interface satisfaction in both directions, multiple
results, errors, pointers, generics, and callbacks. The output is compared to a
golden file, and the generated Go must also pass `go vet`.

## 10. Compatibility matrix

Each row names the test that demonstrates it.

| Construct | Status | Test |
|---|---|---|
| Call Go functions, variadic, spread | ✅ | `run/basics`, `run/advanced` |
| Construct Go structs (named fields), read and write exported fields | ✅ | `run/interop_http` (`&http.Server(...)`), `run/advanced` (`fstest.MapFile`) |
| Value and pointer receiver methods on Go types (incl. on locals) | ✅ | `run/interop_interfaces` (`strings.Builder`), `run/advanced` (`sync.WaitGroup`) |
| Go `(T, error)` with `?`, `catch`, destructuring | ✅ | `run/errors`, `TestThirdPartyModule` |
| Lyth types implementing Go interfaces (`http.Handler`, `fmt.Stringer`, `error`, `sort.Interface`) | ✅ | `run/interop_http`, `run/interop_interfaces` |
| Go values through Lyth interfaces, interface embedding | ✅ | `run/interop_interfaces`, `run/advanced` |
| Generic Go functions with inference, incl. core-type inference for lambdas (`slices.SortFunc`) | ✅ | `run/collections`, `run/lambdas` |
| Explicit type arguments on generic Go functions, called or as values (`errors.AsType[*E](err)`, `pgx.RowToStructByName[T]`) | ✅ | `examples/todoapi` |
| Lambdas converted to named Go function types (`http.HandlerFunc({ w, r -> ... })`) | ✅ | `examples/todoapi` |
| Nullable Lyth values passed to Go parameters (`*T` to `...any`, read as NULL by pgx) | ✅ | `pgstore_test.lyth` (ADR 0017) |
| Go constants (typed and untyped) and package variables | ✅ | `run/interop_http` (`time.Second`), `TestThirdPartyModule` (`uuid.Nil`) |
| Callbacks: Lyth lambdas as Go funcs, incl. fallible callbacks | ✅ | `run/lambdas`, `run/advanced` |
| Named map types and conversions (`fstest.MapFS(...)`, `[]byte(s)`) | ✅ | `run/advanced`, `run/structs`, `run/slicetypes` |
| `encoding/json` on Lyth structs (auto tags, explicit tags, `json:"-"`) | ✅ | `run/structs` |
| `errors.Is` / `errors.As` with Lyth error types | ✅ | `run/errors`, `run/interop_interfaces` |
| Go 1.22 `ServeMux` patterns, `httptest` | ✅ | `run/interop_http` |
| Third-party modules (`github.com/google/uuid`, `github.com/jackc/pgx/v5`) | ✅ | `TestThirdPartyModule`, `examples/todoapi` |
| `context.Context`, `signal.NotifyContext`, `context.AfterFunc` | ✅ | `examples/todoapi` |
| Hand-written `_test.go` files in a Lyth package | ✅ | `TestLythTest`, `memstore_go_test.go` |
| Channels from Go APIs (`ctx.Done().receive()`, `time.After(d).receive()`) | ✅ | `run/channels` |
| Go iterator functions | ⬜ | E0601 |
| Mixed Go and Lyth non-test files in one package | ⬜ | E0601 |
