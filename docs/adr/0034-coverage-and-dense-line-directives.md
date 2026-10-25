# ADR 0034: Coverage in Lyth positions; `//line` on every line of build output

**Status:** Accepted (amends [0005](0005-source-maps-and-line-directives.md) and [0006](0006-overlay-builds.md))

## Context
`lyth test -cover` and `-coverprofile` failed for every package with
`cover: open .../x.lyth.go: no such file or directory`, while the testing guide
advertised `lyth test -race -cover ./...`. `go test -cover` hands each non-test
file of an instrumented package to the cover tool by its path, and the cover
tool reads it from disk: a file that exists only in the overlay (ADR 0006)
fails to open, and an overlaid file that also exists on disk is instrumented
in its disk version (reproduced with plain Go 1.26 and 1.27).

Line numbers were a second problem. Directives were emitted only before lines
of user code that can fail at run time, which keeps generated Go readable and
is all a panic needs. Lines in between inherited drifting numbers: a profile
made from in-tree files named line 21 of a 15-line file, a breakpoint on a
Lyth line could miss, pprof attributed time to the wrong lines, and a vet
finding about the hidden return of a `?` pointed two lines further down.
Directives cannot fix coverage alone, though: the cover tool records physical
positions in the generated file and takes only the file name from them.

## Decision
- **Dense directives in builds.** The files in `.lyth/gen` get a `//line`
  directive wherever the Go numbering would differ from the Lyth line of the
  first mapped node on a line, compiler glue included, so debuggers,
  profilers, and vet see the exact Lyth line of every statement. Nobody reads
  those files. Output for reading (`lyth gen -stdout` and `-in-tree`) keeps
  the sparse form of ADR 0005.
- **Coverage runs write into the tree.** For a run with coverage flags
  (`-cover`, `-covermode`, `-coverpkg`, `-coverprofile`), `lyth test` writes
  the generated non-test files beside their sources and removes them when
  `go test` exits, also after an interrupt. A file left by `lyth gen -in-tree`
  is restored afterwards, and a file of that name that Lyth did not generate
  stops the run. This is the one exception to ADR 0006's rule that builds never
  write into the source tree.
- **Profiles in Lyth positions.** After `go test` exits, `lyth test` rewrites
  the profile through the exact source map of ADR 0005. Each block becomes the
  range of the Lyth code its outermost nodes came from. Blocks that land on the
  same range (the error check and the return of a postfix `?`) merge and count
  as covered only if all of them ran. Nested ranges are split so that every
  byte belongs to its innermost block, because profiles describe disjoint
  ranges. Lyth files are named by absolute path: `go tool cover` resolves an
  import-path name with `go list`, which finds no package in a directory of
  Lyth files.
- **`lyth cover`.** `go tool cover -func` parses every file of a profile as Go,
  so `lyth cover -func=c.out` computes per-function coverage from Lyth
  declarations. Other modes (`-html`) run `go tool cover`, which reads `.lyth`
  files as text.

## Alternatives
- **A `-toolexec` wrapper** that points the cover tool's inputs at
  `.lyth/gen`: it keeps the tree clean, but depends on the cover tool's
  undocumented arguments and conflicts with a user's own `-toolexec`.
- **Dense directives everywhere**: one form of output, but `lyth gen -stdout`
  becomes harder to read for no gain.
- **Import-path file names in profiles**, as Go writes them: portable across
  machines, but `go tool cover -html` cannot find a package of `.lyth` files.

## Consequences
Coverage works and describes Lyth code: an untested error path of a `?` shows
as an uncovered block, so `lyth cover -func` percentages can differ slightly
from the one `go test` prints, which counts generated Go statements. During a
coverage run, generated `x.lyth.go` files exist beside the sources. Profiles
name files by absolute path, so a coverage service needs the checkout's path
mapped in CI. Debuggers, profilers, and vet see exact Lyth lines in builds,
and output for reading is unchanged. Tests: `TestLythTestCover`, and
`checkLineDirectives` in `TestRun`, which asserts that every generated line
starting with Lyth code reports that code's line.
