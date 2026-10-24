---
name: lyth
description: Write, fix, review and explain code in Lyth, the programming language that compiles to Go (`.lyth` files, the `lyth` command). Use this skill for any work on `.lyth` files or Lyth modules, for translating Go or Kotlin code to Lyth, and whenever the user mentions Lyth, even for a one-line change, because Lyth mixes Go's and Kotlin's spellings in its own way and its syntax changes between versions.
---

# Lyth

Lyth compiles to plain Go and builds with the `go` command. It keeps Go's semantics and most of
Go's spelling, and adds error propagation, nullable types, expression `if`/`when`, lambdas, and
named arguments. What you remember about Lyth is probably incomplete, and may describe an older
version: the language changes from release to release. The `lyth` command carries a guide to
its own version, so take the rules from there, not from memory or from this file.

## Before writing Lyth

1. Run `lyth guide` and read all of it. It shows how to work with the compiler, the language
   at a glance, what is spelled differently from Go and from Kotlin, and the syntax that
   changed between versions.
2. Look up details as you need them: `lyth guide topics` lists the documentation pages built
   into `lyth` (the language reference, Go interop, testing, ...), and `lyth guide <topic>`
   prints one.

If `lyth guide` is an unknown command, the installed `lyth` predates the guide: ask the user to
update it (`brew upgrade lyth`, or `go install github.com/lyth-lang/lyth/cmd/lyth@latest`). If
`lyth` isn't installed at all, ask before installing it.

## While writing

* Change a little, then run `lyth check`. Errors point at `.lyth` lines and explain the problem
  in Lyth terms; when a `hint:` gives an edit that matches what you meant, apply it.
* When `lyth check` says that `lyth fix` can rewrite some errors, run `lyth fix`: it changes
  older Lyth syntax, and Go and Kotlin habits, to today's spelling. Existing code in a project
  may predate the installed Lyth; follow the guide, not the old code around you.
* `lyth gen -stdout <file.lyth>` shows the Go a file becomes. Use it when you aren't sure what
  a construct does.
* Finish with `lyth run` and `lyth test ./...`.
* Keep the work in `.lyth` files. Don't edit the generated Go in `.lyth/`, and don't route
  around a Lyth error with `.go` files: Lyth errors have Lyth fixes, and the guide shows them.
