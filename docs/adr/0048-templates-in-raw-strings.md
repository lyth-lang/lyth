# ADR 0048: `${name}` in a raw string warns when it names a variable

**Status:** Accepted (2026-10-27). Extends [0042](0042-dollar-names-in-strings.md)
to raw strings; adds a warning. Not breaking.

## Context

Raw strings use backticks and never interpolate, as in Go. The two languages
whose `${...}` Lyth's templates resemble both interpolate there: a
TypeScript template literal is a backtick string, and Kotlin's raw strings
(`"""..."""`) take templates. A `"..."` string cannot span lines, and the
error that says so suggests a raw string, so multi-line text, such as SQL,
reaches backticks first:

```kotlin
q := `SELECT * FROM orders
      WHERE customer = '${name}'`   // the query contains the text ${name}
```

The program compiles and the query is wrong, with no diagnostic. ADR 0042
catches the Kotlin habit `"$name"` with E0006 when `name` is in scope, and
left raw strings out.

## Decision

- **New warning, W0012.** In a raw string, `${path}` warns when `path` is a
  name or a selector path (`${u.Name}`, `${self.ID}`) whose first name is a
  variable, parameter, or constant in scope, ADR 0042's rule:

  > warning[W0012]: ${name} prints literally: raw strings do not interpolate
  > hint: to insert name, use a "..." string, in which ${name} interpolates
  > (with \n for line breaks), or concatenate: \`...\` + name + \`...\`

- **What stays quiet.** Any other `${...}` is text: names not in scope
  (`${HOME}`, `${PATH}`), shell expressions (`${HOME:-/tmp}`, `${#arr}`),
  calls, and a `${` written after `\` or `$`, the escapes of shell and
  Terraform templates.
- **A warning, not an error.** A raw string has no escape, so an error would
  leave no way to write the text. To keep `${name}` as text while `name` is in
  scope, write it in a `"..."` string as `\${name}`, or rename the variable.

## Alternatives

- **Interpolate in raw strings.** Kotlin does, but it changes the meaning of
  every Go raw string that contains `${`: shell scripts, Terraform, and
  JavaScript embedded in Go programs. Raw strings would need an escape, which
  Go's do not have.
- **An error, as E0006.** Without an escape it cannot be satisfied in place.
- **Status quo.** The habit stays silent, and the hint for a multi-line
  `"..."` string leads to it.

## Consequences

The TypeScript and Kotlin habit is caught where the name could have been
meant. A string that contains `${x}` for another engine warns only if `x`
also names a variable there.

### Implementation
- **Checker.** `rawTemplates` in `internal/check/expr.go`, next to E0006's
  `dollarNames`, runs on every raw string literal.
- **Tests.** `check/rawtemplate_warn`: locals, parameters, constants,
  package-level variables, `self`, lambda parameters, selector paths, and the
  quiet cases.
