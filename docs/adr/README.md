# Architecture Decision Records

Each ADR records one decision: context, decision, alternatives, consequences.
Accepted ADRs are immutable; a superseding ADR links back.

| ADR | Title | Status |
|---|---|---|
| [0001](0001-compile-to-go-source.md) | Compile to Go source through `go/ast` | Accepted |
| [0002](0002-implementation-language-and-deps.md) | Compiler in Go; dependencies | Accepted |
| [0003](0003-go-type-information.md) | Go type information via `go/packages` | Accepted |
| [0004](0004-two-level-type-checking.md) | Lyth checker plus `go/types` validation | Accepted |
| [0005](0005-source-maps-and-line-directives.md) | Exact source maps plus column-less `//line` | Accepted (directives in builds and coverage amended by 0034) |
| [0006](0006-overlay-builds.md) | Overlay builds; in-tree generation on request | Accepted (coverage runs amended by 0034) |
| [0007](0007-val-var-and-immutability.md) | `val`/`var` and value immutability | Superseded for locals by 0031 |
| [0008](0008-error-handling.md) | Error handling: `throws`, `?`, `catch`, `throw` | Accepted |
| [0009](0009-nullability.md) | Nullability with platform types and boxed optionals | Accepted (smart casts amended by 0031) |
| [0010](0010-naming-and-visibility.md) | Verbatim Go names; `pub` and capitalization | Superseded by 0022 |
| [0011](0011-type-syntax.md) | Type syntax: generics-style collections, loose `?` | Accepted (`List<T>` superseded by 0023; other type spellings by 0031) |
| [0012](0012-rejected-features.md) | Features considered and rejected | Accepted (extended by 0031, which adopts `:=`) |
| [0013](0013-struct-fields-and-json.md) | Public fields by default; automatic `json` tags | Amended by 0022 |
| [0014](0014-testing.md) | Tests are Go tests (`lyth test`) | Accepted |
| [0015](0015-backend-on-go-libraries.md) | Backend services use Go libraries directly | Accepted |
| [0016](0016-type-tests.md) | Type tests are type assertions, with smart casts | Accepted (smart casts amended by 0031) |
| [0017](0017-nullable-values-into-go.md) | Nullable values may be passed to Go APIs | Accepted (refines 0009) |
| [0018](0018-typed-catch.md) | Typed `catch` clauses match with `errors.AsType` | Accepted (extends 0008) |
| [0019](0019-named-and-default-arguments.md) | Named and default arguments for Lyth functions | Accepted |
| [0020](0020-declared-conformance.md) | Declared conformance: `struct T : I` | Accepted |
| [0021](0021-inferred-results-and-pass-through.md) | Inferred results for private expression bodies; pass-through `return` | Accepted (refines 0008) |
| [0022](0022-capitalization-is-visibility.md) | Capitalization is visibility; Lyth names are Go names | Accepted (supersedes 0010) |
| [0023](0023-slice-types.md) | Slice types are spelled `[]T` | Accepted (amended by 0031) |
| [0024](0024-channel-operations.md) | Channel operations are methods; `select` | Accepted (types amended by 0031) |
| [0025](0025-enums-and-sealed-interfaces.md) | Enums and sealed interfaces | Accepted |
| [0026](0026-null-is-a-nil-interface.md) | Null converts to a nil interface | Accepted |
| [0027](0027-lambda-fallibility.md) | Only `throws` and Go callbacks make a lambda fallible | Accepted (refines 0008) |
| [0028](0028-generic-declarations.md) | Generic declarations are Go generics | Accepted (spelling amended by 0031) |
| [0029](0029-extension-functions.md) | Extension functions are Go functions called like methods | Accepted (spelling amended by 0031) |
| [0030](0030-standard-library.md) | The standard library is compiled with the program | Accepted |
| [0031](0031-go-spelling.md) | Go's spelling for what Go already has: `func`, `:=`, Go types, `[T]`, headers, `self` | Accepted (supersedes 0007 for locals and parts of 0011; amends 0009, 0012, 0016, 0023, 0024, 0028, 0029) |
| [0032](0032-typed-literals.md) | Go's typed literals for slices, arrays, and maps | Accepted (extends 0031) |
| [0033](0033-agents-learn-from-the-compiler.md) | Coding agents learn Lyth from the installed compiler: `lyth guide`, `lyth fix` | Accepted |
| [0034](0034-coverage-and-dense-line-directives.md) | Coverage in Lyth positions; `//line` on every line of build output | Accepted (amends 0005, 0006) |
