# AI coding agents

Lyth is built to be written by AI coding agents as well as by people. A model has seen little
Lyth, and what it has seen may describe an older version, so Lyth does not rely on what the
model remembers. The installed compiler teaches the language, every project tells its agent
where to look, and the compiler corrects what the model gets wrong:

- **`lyth guide` teaches the installed version.** It prints the guide to writing Lyth that is
  built into the `lyth` binary: how to work with the compiler, the language at a glance, what
  Lyth spells differently from Go and from Kotlin, and the syntax that changed between
  versions. It always matches the compiler that runs the code, because it is part of it.
- **Every project points its agent at the guide.** `lyth init` writes an `AGENTS.md` (and a
  `CLAUDE.md` that imports it, for Claude Code) telling agents to read `lyth guide` and check
  their work with `lyth check`. There is also a Claude Code plugin that does the same for any
  Lyth work.
- **The compiler corrects mistakes with exact edits.** Diagnostics point at `.lyth` lines,
  speak Lyth, and end with the edit to make. `lyth fix` applies every edit that only respells
  code: older Lyth syntax, and the Go and Kotlin habits models bring with them.
- **What an agent writes is easy to review.** Using a value that may be nil without a check,
  or leaving an error unhandled, is a compile error, and `lyth gen -stdout` shows the plain Go
  any change compiles to.

## Set up

Install Lyth (`brew install lyth-lang/tap/lyth`, or [other ways](tooling.md#installing)), then give
your agent a project it can learn from.

**A new project.** `lyth init` makes the current directory a Lyth module with everything an
agent needs:

```bash
mkdir hello && cd hello
lyth init example.com/hello
```

It writes `go.mod`, a `main.lyth` to start from, `AGENTS.md`, `CLAUDE.md` (one line,
`@AGENTS.md`), and `.lyth/` in `.gitignore`. Then ask your agent for what you want built.

**An existing project.** Run `lyth init` in the module's root directory. In a module that
already exists it adds only what is missing, such as `AGENTS.md` and `CLAUDE.md`, and changes
nothing else.

**Claude Code.** Install the Lyth plugin once, and Claude Code loads its skill for any work on
`.lyth` files, in every project:

```bash
claude plugin install lyth --marketplace lyth-lang/lyth
```

Inside Claude Code, the same is `/plugin marketplace add lyth-lang/lyth` followed by
`/plugin install lyth@lyth`.

**Other agents.** `AGENTS.md` is the convention that Codex, Cursor, GitHub Copilot, and many
other coding agents read from the project's root; the measurements below use Claude Code. For an agent that reads another file, add the
same instruction to it: *run `lyth guide` before writing Lyth, and `lyth check` after each
change.*

## The commands agents use

| Command | What it gives the agent |
|---|---|
| `lyth guide` | The guide to writing the installed version of Lyth, with the version that printed it. |
| `lyth guide topics` | The documentation pages built into `lyth`: the tour, Lyth for Go programmers, the cheat sheet, Go interop, testing, tooling, the language reference, types, errors, collections. |
| `lyth guide <topic>` | One of those pages, such as `lyth guide interop` or `lyth guide errors`. |
| `lyth check` | Type-checks the module, test files included, without building. Fast enough to run after every change. |
| `lyth check -diagnostics=json` | The same diagnostics as JSON lines, each edit with `"fix": true` when `lyth fix` would apply it. |
| `lyth fix` | Applies every fix across the module, compiling again until none is left, then reports the errors that remain. |
| `lyth run` / `lyth test ./...` | Runs the program or the tests; failures and panics point at `.lyth` lines. |
| `lyth gen -stdout file.lyth` | The Go a file compiles to, for when the agent (or its reviewer) is unsure what a construct does. |
| `lyth lsp` | The language server, for agents that work through an editor. |

## What the compiler tells an agent

A model that half-remembers Lyth tends to write Kotlin and older Lyth into it:

```kotlin
fun greet(name: String): String {
    val msg = "hello, $name"
    return msg
}
```

`lyth check` reports each mistake at its line, with the edit:

```text
main.lyth:1:1: error[E0008]: functions are declared with func
  |
1 | fun greet(name: String): String {
  | ^^^
  |
  = hint: write func

main.lyth:2:23: error[E0006]: string templates use ${...}; "$name" would print literally
  |
2 |     val msg = "hello, $name"
  |                       ^^^^^
  |
  = hint: write ${name} to interpolate, or \$name for a literal dollar sign

5 errors
lyth fix rewrites 5 of them: they are older Lyth syntax, or Kotlin or Go spellings
```

`lyth fix` applies all five, and the program builds:

```kotlin
func greet(name: string): string {
    msg := "hello, ${name}"
    return msg
}

func main() {
    println(greet("agent"))
}
```

Fixes only respell code. Edits that would change what a program does, such as adding `?` to
propagate an error or converting a number, stay hints for the agent to decide on.

## When Lyth changes

Lyth's syntax changes between releases, and will until 1.0. Three things keep agents current
([ADR 0033](../adr/0033-agents-learn-from-the-compiler.md)):

- `AGENTS.md`, `CLAUDE.md`, and the Claude Code skill hold no language rules, only commands, so
  they never go stale. Updating `lyth` updates what every agent learns.
- When a release changes syntax, the older form keeps a diagnostic with a fix, and the guide's
  [changes between versions](writing-lyth.md#changes-between-versions) gains a row. An agent
  that writes the old form is corrected by `lyth check`, whether or not it read the guide.
- After an upgrade, `lyth fix` rewrites a module's older syntax, so agents don't copy it from
  the code around them.

## Measured

[`evals/`](../../evals) gives programming tasks to Claude Code and grades the programs: 12 tasks
covering errors, nullable types, generics, enums, packages, tests, goroutines, porting Go, and
upgrading code in older syntax. On 2026-10-25, with Claude Code 2.1.301 and its `sonnet` model,
three runs of each task in each condition (36 runs per column):

| | `AGENTS.md` | Claude Code skill | `lyth help` only | no help |
|---|---|---|---|---|
| tasks passed | 36/36 | 36/36 | 36/36 | 36/36 |
| first build succeeded | 27/36 | 26/36 | 8/36 | 0/36 |
| builds per run | 1.5 | 1.5 | 2.8 | 4.2 |
| turns per run | 6.5 | 8.6 | 11.3 | 14.2 |
| time per run | 18 s | 19 s | 26 s | 31 s |

Every condition gets there in the end, because the compiler's diagnostics guide even an agent
with no help. With `lyth guide` (through the `AGENTS.md` that `lyth init` writes, or the skill),
three first builds in four succeed instead of none, with about a third of the builds and fewer
turns (less than half with `AGENTS.md`). To run the evals yourself:

```bash
python3 evals/run.py --reps 3 --conditions skill,agents-md,cli,bare
```

They make real model calls; the [evals README](../../evals/README.md) explains the tasks, the
grading, and the cost.

## See also

- [Writing Lyth](writing-lyth.md): the page `lyth guide` prints.
- [Tooling](tooling.md): every command and flag, and `lyth fix` in detail.
- [Diagnostics](../design/diagnostics.md): how errors are reported and fixed.
