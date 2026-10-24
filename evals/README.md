# Evals: how well models write Lyth

Models know little about Lyth, and what they know may be from an older version. Lyth helps them
with `lyth guide` (the guide to the installed version, built into the binary), the `AGENTS.md`
and `CLAUDE.md` that `lyth init` writes, `lyth fix` (which rewrites older syntax), and the
Claude Code skill in [`skills/lyth`](../skills/lyth/SKILL.md). `run.py` measures how much each
of these helps: it gives programming tasks to Claude Code and checks the programs it writes.

```bash
python3 evals/run.py                          # every task, with the skill and without help, on Sonnet
python3 evals/run.py --model opus --reps 3    # another model, three runs of each task
python3 evals/run.py --conditions skill,agents-md,cli,bare
python3 evals/run.py --mode oneshot           # one answer without tools: what the guide alone teaches
python3 evals/run.py --tasks upgrade-grades,fix-the-build
python3 evals/run.py --check                  # no model calls: every reference solution passes its task
python3 evals/show.py evals/results/<run>/runs/<task>/<condition>-1   # the tool calls of one run
```

It needs Python 3.11 or newer, Go 1.26 or newer, a shell (macOS or Linux), and Claude Code
logged in (`claude`). The runs are real model calls, billed to your Claude account or API key;
the report shows what they cost. Results go to `evals/results/<date>-<model>-<mode>/` (ignored by
git): `report.md`, `results.jsonl`, and for each run its transcript, the sources it ended with,
and its grade. `--report <dir>` rewrites a report from `results.jsonl`, counting the Go and
Kotlin habits in the saved sources again.

## What a run does

1. `run.py` builds `lyth` from this checkout into a temporary directory, so the runs use this
   checkout's compiler without pointing at the repository and its documentation.
2. For each task and condition, it copies the task's starter files (if any) into a new
   directory and runs `lyth init app` there, which writes `go.mod`, `main.lyth` (for tasks
   without a starter), `AGENTS.md` and `CLAUDE.md`. It gives the task to `claude -p` there.
   The agent can use Bash (`lyth`, `go doc`, and read-only commands), Read, Write, Edit, Glob,
   Grep and Skill. Your own settings, CLAUDE.md files, memory, plugins and MCP servers aren't
   loaded, and the system prompt asks the agent to work from what it knows, its skills and the
   `lyth` command, without looking for Lyth's documentation or sources elsewhere.
3. It copies the `.lyth` files the agent left into a fresh module (so `.go` files and changes
   to `go.mod` don't count), checks, runs and tests it, and grades it.

| Condition | What the agent gets besides the task |
|---|---|
| `skill` | the skill in `skills/lyth` (or `--skill <dir>`), which sends it to `lyth guide` |
| `agents-md` | the `AGENTS.md` and `CLAUDE.md` that `lyth init` writes, which send it to `lyth guide` |
| `cli` | nothing, but `lyth help` lists `lyth guide` |
| `bare` | nothing, and `lyth guide` is hidden: what the model knows by itself, plus the compiler's diagnostics |

In `--mode oneshot` the model answers once, without tools: `skill` puts what `lyth guide`
prints into the system prompt, and `bare` gives only the task.

A run **passes** when the module type-checks (`lyth check`, test files included) and prints
exactly the task's `expected.out`; uses what the task asks for (`requires` in `task.toml`) and
nothing it forbids (`forbids`); has the files the task names; passes `lyth test` with at least
`tests` tests; and doesn't print the expected output literally.

The report also shows, for each condition: how many first builds succeeded, builds, turns,
cost and time per run, the Go and Kotlin habits per solution (Go or Kotlin syntax where Lyth
has its own form: `func (r T)`, `range`, `<-`, `nil`, `T{F: v}`, `fun`, `val`, `this`, `Int`,
`List<T>`, `<T>`, `"$name"`, ...), how many runs read `lyth guide`, ran `lyth fix`, and loaded
the skill, and the runs that looked outside their project anyway, which are listed with what
they read.

## Tasks

Each directory in `tasks/` is one task:

| File | |
|---|---|
| `prompt.md` | the request, written as a user would, ending with the exact output |
| `expected.out` | what the program must print |
| `task.toml` | `features` it exercises, `requires` / `forbids` (regexes over the code without comments; `forbids` also without strings), `files` it must have (a path or a directory prefix such as `units/`), and the number of `tests` `lyth test` must pass |
| `solution/` | a reference solution, never shown to the model |
| `starter/` | files copied into the module before the run, for tasks that change existing code |

`upgrade-grades` is the upgrade case: its starter is written in Lyth's syntax from before
0.1.0 (`fun`, `val`, `Int`, `List<T>`, `this`, `"$name"`), it no longer builds, and the
request is a new feature that doesn't mention the upgrade. It forbids the older forms anywhere
in the module, so it passes only when the agent brings the old code up to date (`lyth fix` does
that) and writes the new code in today's syntax rather than copying the old.

`go test ./tests -run TestEvalSolutions` builds and runs every reference solution, and checks
that every starter fails to compile; `run.py --check` also grades them. So a task whose
solution stops building or printing its `expected.out` is noticed. To add a task, write and run
the solution first, save what it prints as `expected.out`, and then write the prompt. Keep the
output deterministic: a map iterates in a different order every run, so sort before printing.
