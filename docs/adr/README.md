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
| [0007](0007-val-var-and-immutability.md) | `val`/`var` and value immutability | Superseded for locals by 0031; parameters amended by 0039 |
| [0008](0008-error-handling.md) | Error handling: `throws`, `?`, `catch`, `throw` | Accepted |
| [0009](0009-nullability.md) | Nullability with platform types and boxed optionals | Accepted (smart casts amended by 0031; optional values by 0049) |
| [0010](0010-naming-and-visibility.md) | Verbatim Go names; `pub` and capitalization | Superseded by 0022 |
| [0011](0011-type-syntax.md) | Type syntax: generics-style collections, loose `?` | Accepted (`List<T>` superseded by 0023; other type spellings by 0031) |
| [0012](0012-rejected-features.md) | Features considered and rejected | Accepted (extended by 0031, which adopts `:=`; `$name` amended by 0042) |
| [0013](0013-struct-fields-and-json.md) | Public fields by default; automatic `json` tags | Amended by 0022 and 0036 |
| [0014](0014-testing.md) | Tests are Go tests (`lyth test`) | Accepted |
| [0015](0015-backend-on-go-libraries.md) | Backend services use Go libraries directly | Accepted |
| [0016](0016-type-tests.md) | Type tests are type assertions, with smart casts | Accepted (smart casts amended by 0031; warning on errors added by 0041) |
| [0017](0017-nullable-values-into-go.md) | Nullable values may be passed to Go APIs | Accepted (refines 0009; amended by 0040) |
| [0018](0018-typed-catch.md) | Typed `catch` clauses match with `errors.AsType` | Accepted (extends 0008; confirmed by 0041) |
| [0019](0019-named-and-default-arguments.md) | Named and default arguments for Lyth functions | Accepted |
| [0020](0020-declared-conformance.md) | Declared conformance: `struct T : I` | Accepted |
| [0021](0021-inferred-results-and-pass-through.md) | Inferred results for private expression bodies; pass-through `return` | Accepted (refines 0008) |
| [0022](0022-capitalization-is-visibility.md) | Capitalization is visibility; Lyth names are Go names | Accepted (supersedes 0010; json keys amended by 0036) |
| [0023](0023-slice-types.md) | Slice types are spelled `[]T` | Accepted (amended by 0031) |
| [0024](0024-channel-operations.md) | Channel operations are methods; `select` | Accepted (types amended by 0031) |
| [0025](0025-enums-and-sealed-interfaces.md) | Enums and sealed interfaces | Accepted (extended by 0035) |
| [0026](0026-null-is-a-nil-interface.md) | Null converts to a nil interface | Accepted (extended by 0040) |
| [0027](0027-lambda-fallibility.md) | Only `throws` and Go callbacks make a lambda fallible | Accepted (refines 0008) |
| [0028](0028-generic-declarations.md) | Generic declarations are Go generics | Accepted (spelling amended by 0031) |
| [0029](0029-extension-functions.md) | Extension functions are Go functions called like methods | Accepted (spelling amended by 0031) |
| [0030](0030-standard-library.md) | The standard library is compiled with the program | Accepted |
| [0031](0031-go-spelling.md) | Go's spelling for what Go already has: `func`, `:=`, Go types, `[T]`, headers, `self` | Accepted (supersedes 0007 for locals and parts of 0011; amends 0009, 0012, 0016, 0023, 0024, 0028, 0029; amended by 0037, 0038, 0039, 0044, 0046) |
| [0032](0032-typed-literals.md) | Go's typed literals for slices, arrays, and maps | Accepted (extends 0031) |
| [0033](0033-agents-learn-from-the-compiler.md) | Coding agents learn Lyth from the installed compiler: `lyth guide`, `lyth fix` | Accepted |
| [0034](0034-coverage-and-dense-line-directives.md) | Coverage in Lyth positions; `//line` on every line of build output | Accepted (amends 0005, 0006) |
| [0035](0035-enums-on-the-wire.md) | Enums travel by name, and string-backed enums | Accepted (amends 0025) |
| [0036](0036-automatic-json-tags.md) | Automatic `json` tags: `omitzero` for optional fields, initialisms, tags without a name | Accepted (amends 0013, 0022) |
| [0037](0037-type-declarations.md) | `type` declares defined types and aliases | Accepted (amends 0031) |
| [0038](0038-members-through-self.md) | Members are reached through `self.` | Accepted (amends 0031) |
| [0039](0039-rebinding-parameters.md) | `:=` may replace a parameter with a value derived from it | Accepted (amends 0007, 0031) |
| [0040](0040-optionals-into-any.md) | Optional values passed to Go `any` parameters are unboxed | Accepted (amends 0017, extends 0026) |
| [0041](0041-type-tests-on-errors.md) | Type tests on errors stay dynamic, and warn | Accepted (confirms 0016, 0018) |
| [0042](0042-dollar-names-in-strings.md) | `$name` in a string is literal unless it names a variable | Accepted (amends 0012; extended by 0048) |
| [0043](0043-struct-embedding.md) | Struct embedding | Accepted |
| [0044](0044-remaining-spellings.md) | Remaining spellings: `null`, import aliases, and `in` as a name | Accepted (amends 0031) |
| [0045](0045-go-evaluation-order.md) | Go's evaluation order, with and without hoisting | Accepted (replaces language.md §6's rule) |
| [0046](0046-smart-casts-between-assignments.md) | Smart casts between assignments | Accepted (amends 0031) |
| [0047](0047-go-constructs-lyth-lacked.md) | The Go constructs Lyth lacked: three-part `for`, `if` init, groups with `iota`, named results, type sets, complex numbers, generic aliases | Accepted (amends 0012, 0031) |
| [0048](0048-templates-in-raw-strings.md) | `${name}` in a raw string warns when it names a variable | Accepted (extends 0042) |
| [0049](0049-optional-values-are-copies.md) | Optional values are copies of a pointer's value: `*p`, no implicit `*T` to `T?` | Accepted (amends 0009) |
| [0050](0050-safe-call-only.md) | `?.` is only the safe call; `(f()?).name` propagates | Accepted |
| [0051](0051-returns-in-catch-handlers.md) | A bare `return` in a `catch` of a fallible lambda warns | Accepted |
