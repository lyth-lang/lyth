# Testing

Lyth tests are Go tests. They live in `_test.lyth` files beside the code they
test, use Go's `testing` package, and run with `lyth test`, which compiles the
package with its tests and hands them to `go test`. Everything that works with
`go test` works: `-run`, `-v`, `-race`, `-cover`, benchmarks, fuzzing, and CI
reporters. [ADR 0014](../adr/0014-testing.md) explains the choice.

## A first test

```kotlin
import "strings"
import "testing"

// Normally in slug.lyth; a test file may declare helpers too.
func slug(title: string): string = strings.ToLower(strings.Join(strings.Fields(title), "-"))

func TestSlug(t: *testing.T) {
    got := slug("  Hello Lyth World ")
    if got != "hello-lyth-world" {
        t.Errorf("slug = %q, want %q", got, "hello-lyth-world")
    }
}
```

Functions named the way `go test` expects (`TestXxx`, `BenchmarkXxx`,
`FuzzXxx`, `ExampleXxx`, and `TestMain`) are capitalized, so they are exported
as `go test` requires. Their signatures are checked
(`func TestXxx(t: *testing.T)` and so on), so a typo in a signature cannot
silently skip a test. An example's `// Output:` comment at the end of its body
is kept in the generated Go, so `go test` runs the example and compares what
it prints.

## Tests that throw

```kotlin
import "strconv"
import "testing"

func parsePort(s: string): int throws {
    n := strconv.Atoi(s)?
    if n < 1 || n > 65535 {
        throw strconv.ErrRange
    }
    return n
}

func TestParsePort(t: *testing.T) throws {
    port := parsePort("8080")?
    if port != 8080 {
        t.Fatalf("port = %d", port)
    }
}
```

A test, benchmark, or fuzz target may be declared `throws`, so setup code can
use `?` instead of an error check per step. The Go tab shows the wrapper Lyth
generates: when the body throws, the test fails with the error, reported at
the test's declaration line.

## Table-driven tests

```kotlin
import "testing"

func grade(score: int): string = when {
    score >= 90 -> "A"
    score >= 75 -> "B"
    score >= 50 -> "C"
    else -> "F"
}

func TestGrade(t: *testing.T) {
    struct gradeCase {
        Score: int
        Want: string
    }
    cases := [gradeCase(95, "A"), gradeCase(80, "B"), gradeCase(50, "C"), gradeCase(10, "F")]
    for c in cases {
        t.Run("score ${c.Score}") { st ->
            if grade(c.Score) != c.Want {
                st.Errorf("grade(%d) = %s, want %s", c.Score, grade(c.Score), c.Want)
            }
        }
    }
}
```

The case type is declared in the test, as Go's local `type gradeCase struct`;
Lyth has no anonymous struct types, so `[]struct{ ... }{...}` is written this
way. Subtests are `t.Run` with a trailing lambda. Loop variables are fresh per
iteration (Go 1.22 semantics), so closures capture the right case.

## HTTP handlers

Handlers are tested the Go way, with `net/http/httptest` and no network. The
[REST API example](../../examples/todoapi/README.md) tests its whole API this
way against an in-memory store; its `api_test.lyth` is a fuller model than any
snippet here.

## Go test files beside Lyth code

A package may contain hand-written `_test.go` files next to its `.lyth` files.
Only `go test` compiles them, and they use the package's Go API, which shows
what Go callers of the package see. The REST API example's
`memstore_go_test.go` tests the Lyth in-memory store from plain Go.

## Integration tests

Tests that need a database or another service can skip when it is not
configured:

```kotlin
import "os"
import "testing"

func TestWithDatabase(t: *testing.T) throws {
    url := os.Getenv("DATABASE_URL")
    if url == "" {
        t.Skip("DATABASE_URL is not set")
    }
    // connect, migrate, and test against the real database
}
```

## Benchmarks

```kotlin
import "strings"
import "testing"

func BenchmarkRepeat(b: *testing.B) {
    while b.Loop() {
        strings.Repeat("lyth", 100)
    }
}
```

Benchmarks run with `lyth test -bench . -benchmem` and compare with
`benchstat` like Go benchmarks. The suite that compares Lyth with hand-written
Go is described in [benchmarks](../design/benchmarks.md).

## Running tests

```bash
lyth test ./...                       # all packages in the module
lyth test -v -run TestSlug ./text     # go test flags work as usual
lyth test -race -cover ./...
DATABASE_URL=postgres:///test lyth test -run TestWithDatabase ./store
```

Flags other than Lyth's own (`-tags`, `-diagnostics`, `-no-line-directives`, `-timings`) go to
`go test`. Failures point at `.lyth` lines:

```text
--- FAIL: TestSlug (0.00s)
    slug_test.lyth:10: slug = "hello-lyth--world", want "hello-lyth-world"
```

`lyth build` and `lyth run` ignore test files; `lyth check`, `lyth vet`, and
`lyth gen` include them.

A test file may also be an external test package, as in Go: `package
slug_test` imports the package under test and sees what it exports, which is
how examples read in `go doc`:

```kotlin
package slug_test

import "fmt"
import "example.com/app/slug"

func ExampleMake() {
    fmt.Println(slug.Make("Hello Lyth"))
    // Output: hello-lyth
}
```

## Coverage

`-cover`, `-covermode`, `-coverpkg`, and `-coverprofile` work as with `go test`.
A profile written by `lyth test` describes your `.lyth` files: every block is a
range of Lyth code, and Lyth files are named by absolute path.

```bash
lyth test -coverprofile=c.out ./...
go tool cover -html=c.out             # Lyth source, highlighted
lyth cover -func=c.out                # coverage of each function
```

```text
calc/calc.lyth:5:       Add             100.0%
calc/calc.lyth:8:       Parse           60.0%
calc/calc.lyth:16:      Sign            50.0%
total:                  (statements)    60.0%
```

Use `lyth cover -func` rather than `go tool cover -func`, which parses every file
in the profile as Go and fails on `.lyth` files. `lyth cover -html=c.out` runs
`go tool cover -html`. The error path of a postfix `?` is a block of its own,
covered only when a test made the call fail, so an untested error path shows
in the report. The percentages `lyth cover -func` prints can therefore differ
slightly from the one `go test` prints, which counts the generated Go
statements.

The Go cover tool reads the files it instruments from disk and ignores the
overlay, so for a run with coverage flags `lyth test` writes the generated
`x.lyth.go` files beside their sources and deletes them when `go test` exits,
also on Ctrl-C. This is the only time Lyth writes into the source tree. A file
left by `lyth gen -in-tree` is restored afterwards, and a file with that name
that Lyth did not generate stops the run. With Go 1.26, whose cover tool reads the
`//line` directives (which have no columns) into the profile, these files are
written without directives, so a panic in a package under test points at
`x.lyth.go` lines during a coverage run; the profile still ends up in Lyth
positions. Go 1.26 also counts some statements differently from Go 1.27, so
the percentages can differ between the two.
