# ADR 0044: Remaining spellings: `null`, import aliases, and `in` as a name

**Status:** Accepted (2026-10-25). Amends [0031](0031-go-spelling.md)
for import aliases. Breaking: one rare spelling (import aliases); the rest
keeps today's spelling or relaxes a rule.

## Context

ADR 0031 adopted Go's spelling for everything Go has, unless the Lyth spelling
names something Go lacks or changes a meaning Go would get wrong (design
principle 8). The review found three spellings a Go reader will question
first. Each needs a decision before people learn the language.

## Decision

### `null` stays
- **The reason.** In Lyth, `null` belongs to the null-safety vocabulary (`T?`,
  `?.`, `?:`, `!!`), and its conversion rule differs from Go's `nil`: a null
  pointer converted to an interface is a nil interface, never Go's typed nil
  (ADR 0026). Spelling it `nil` would make `err != nil` look like Go while
  meaning something subtly different. ADR 0031 rejected `range` for the same
  false-friend reason.
- **What stays.** `nil` stays recognized with the fix `null`, so agents and Go
  developers keep getting the edit.

### Import aliases use Go's spelling
- **The new form.** `import h "net/http"` and, in a group,
  `import ( h "net/http"; _ "net/http/pprof" )`.
- **Why.** `import "net/http" as h` is a different word for the same concept,
  which principle 8 rules out, and it is the only remaining Lyth spelling of a
  pure Go construct. Aliases are rare, so the change touches few lines.
- **Migration.** `"x" as y` stays recognized with the fix `y "x"`. `lyth fix`
  rewrites it, and the guide's version table changes its import row.

### `in` may name a field or parameter
- **Why.** `in` is a keyword, but Go code uses it as a name constantly:
  - `in`/`want` fields in table-driven tests;
  - generated gRPC clients' `in *Request` parameters;
  - pipeline stages `func stage(in <-chan T, out chan<- T)`.

  Each use is an error today, with a cascade.
- **Where `in` becomes a name.** It becomes a contextual keyword, a name in
  every position where the operator cannot appear:
  - after `.` (`tc.in`);
  - in declarations: fields, parameters, locals, lambda parameters, and named
    arguments (`parseCase(in = "x")`);
  - at the start of an operand (`f(in)`, `in.Name`, `x := in`).
- **Where it stays the operator.** After a complete operand (`x in xs`) and in
  `for … in`.
- **One case stays reserved.** In a `when` branch condition, a leading `in`
  remains the range test (`in 200..299 ->`). A variable named `in` there must
  be written `(in)`.
- **Not breaking.** It only accepts code that is an error today.

## Alternatives

- **`nil` instead of `null`.** It is what Go developers and models type first
  (the evals' most common leftover habit is `!= nil`), and Swift pairs `T?`
  with `nil`. Rejected for the false-friend reason above. This is the closest
  call in this ADR, and the cost of switching now would be internal only:
  docs, tests, and grammars.
- **Keep `import … as`.** It reads naturally. But it is the last exception to
  principle 8, and every Go file uses the other form.
- **Keep `in` fully reserved.** It is simpler for the parser, but it keeps a
  daily paper cut for table tests and gRPC code.

## Consequences

- **Lyth stays Go-like.** Lyth's spelling differs from Go only where its
  meaning does, plus the deliberate list in ADR 0031 (`name: Type`, `when`,
  `while`, channel methods).
- **The `in` rule.** It costs a parser rule and a sentence in the spec.

### Implementation
- **Parser.**
  - Import declarations accept Go's form; the `as` form becomes an E0008 with
    a fix.
  - `in` becomes a contextual identifier in `internal/syntax/parser.go`; the
    tree-sitter and TextMate grammars learn both.
- **Tests.**
  - `fix/imports`, `check/syntax_neg`.
  - `run/` with `in` fields, parameters, and named arguments.
