# ADR 0033: Coding agents learn Lyth from the installed compiler

**Status:** Accepted

## Context
Much of the code written in Lyth will be written by AI coding agents. Models know little about
Lyth, and the little they know may describe an older version: Lyth's syntax changes between
releases, and will keep changing until 1.0. Instructions written into a project (an
`AGENTS.md`) or an agent's setup (a skill) outlive the Lyth version they describe. So does the
code itself, and agents imitate the code around them more readily than any guide: after an
upgrade, a module's own older syntax teaches the old language.

## Decision
- **The guide is in the binary.** `lyth guide` prints `docs/guide/writing-lyth.md`, embedded
  in the `lyth` binary (`docs/docs.go`), with the version that printed it. `lyth guide <topic>`
  prints the other guide and specification pages. Its examples are compiled and run by the docs
  tests like every other documentation page.
- **Everything else only points at it.** The `AGENTS.md` that `lyth init` writes, its
  `CLAUDE.md` (`@AGENTS.md`, for Claude Code), and the Claude Code skill in `skills/lyth` name
  commands, never language rules; tests keep Lyth code out of them. `lyth guide`,
  `lyth guide topics`, `lyth check`, `lyth fix`, `lyth run`, and `lyth test` keep working in
  every later version, because those files call them.
- **Old syntax is a compiler diagnostic with a fix.** A diagnostic edit that only respells what
  the code means is marked as a fix (`diag.Edit.Fix`). `lyth check` counts them; `lyth fix`
  applies them across a module, test files included, compiling again until none is left. When
  a release changes syntax, the old form keeps a diagnostic with a fix for at least the
  following releases, and the guide's "Changes between versions" table gains a row. An agent
  that writes the old form is corrected by `lyth check` whether or not it read the guide, and
  `lyth fix` removes the old code it would otherwise copy.
- **Evals measure it.** `evals/` gives programming tasks to Claude Code with the skill, with
  the `AGENTS.md`, with only the command, and with nothing, and grades the programs. One task
  starts from a module in older syntax.

## Consequences
- A language change also changes `writing-lyth.md`, and a breaking one adds a fix and a row.
- Edits that change behavior (adding `?`, numeric conversions, guessed names) stay hints, so
  `lyth fix` never changes what a program does.
- A Go method receiver, `func (p *T) M()`, has no fix: the body names the receiver, and
  renaming it is left to the author.
- There is no project-level Lyth version: the compiler is the only version that matters, and
  `lyth guide` names it.
