# ADR 0042: `$name` in a string is literal unless it names a variable

**Status:** Accepted (2026-10-25). Amends the `$name` row of
[0012](0012-rejected-features.md) and E0006.
Breaking in the permissive direction: strings that are errors today compile.
The one breaking part is that `lyth fix` stops rewriting them.

## Context

Templates are `"${expr}"` only; ADR 0012 rejected Kotlin's `"$name"` shorthand.
To catch the Kotlin habit, the lexer reports every `$` followed by an
identifier as E0006 ("string templates use ${...}; "$name" would print
literally"). Its edit, `${name}`, is marked as a fix. Two problems:

- **Backend strings use `$word` literally all the time.** MongoDB operators
  (`"$set"`, `"$inc"`, `"$in"`), JSON Schema and OpenAPI (`"$ref"`,
  `"$schema"`, `"$id"`), PostgreSQL dollar quoting (`$body$`), and shell and
  environment snippets (`"$HOME"`) are all errors today. Each must be written
  `\$set`, and a MongoDB update reads `["\$set": …, "\$inc": …]`.
- **The fix changes meaning.** The lexer cannot see scopes, so `lyth fix`
  always rewrites `$name` to `${name}`. In the review, a PostgreSQL trigger
  `AS $body$ … $body$` in a function with a local `body` became
  `AS ${body}$ … ${body}$`. It compiled, and it interpolated the function body
  into the SQL twice. Where no variable exists, the rewrite fails to compile
  instead. The migration policy says a fix only respells what the code means;
  this one guesses.

## Decision

- **No variable in scope: plain text.** A `$` followed by an identifier is
  literal text when the identifier does not name a local, parameter, constant,
  or package-level variable in scope at that string. There is no diagnostic.
- **A variable in scope: E0006, with two hints and no fix.** When the
  identifier does name one, it is still E0006: Kotlin's habit, and very likely
  a mistake.

  > error[E0006]: "$name" prints literally; templates are written ${name}
  > hint: write ${name} to interpolate it, or \$name to keep the dollar sign

  `lyth fix` no longer edits it; the author chooses.
- **Escapes and raw strings.** `\$` keeps working everywhere. Raw strings are
  unchanged, and they never interpolate.
- **The check moves.** It moves from the lexer to the checker, which knows the
  scope. The lexer keeps the text as written.

## Alternatives

- **Status quo, with the fix demoted to a hint.** This stops `lyth fix` from
  corrupting SQL, but every literal `$word` still needs `\$`, the most
  visible friction for MongoDB and JSON Schema code.
- **Always literal, with no diagnostic.** This is the simplest rule, but an
  agent's or a Kotlin developer's `"Hello $name"` would compile and print
  `Hello $name`. That is exactly what E0006 exists to stop.
- **Make `$name` a second template syntax.** ADR 0012 rejected two template
  syntaxes, and that reason still holds.

## Consequences

- **Fewer errors.** MongoDB, JSON Schema, OpenAPI, SQL, and shell strings are
  written as in Go. The Kotlin habit is still caught when it matters, without
  an automatic rewrite.
- **A new variable can break a string.** Adding a variable named `set` to a
  function that contains `"$set"` turns the string into an error. That is
  visible, never silent, and the fix is `\$set`.

### Migration
- **Old code keeps working.** Programs that compile today keep compiling with
  the same meaning: `\$name` still means a literal dollar.
- **`lyth fix` does less.** It stops rewriting E0006, so anyone who relied on
  it gets an error with two hints instead. The guide's version table changes
  its `"$name"` row from "yes" to "no".

### Implementation
- **Lexer and checker.** In `internal/syntax/lexer.go`, record `$name`
  occurrences with their spans instead of reporting them. In
  `internal/check`, report E0006 for the ones that resolve in scope.
- **Tests.**
  - `check/templates_neg`: in-scope and out-of-scope names, parameters,
    constants.
  - `run/` with MongoDB-style keys and a `$body$` SQL string.
  - A `fix/` case showing `lyth fix` leaves E0006 alone.
