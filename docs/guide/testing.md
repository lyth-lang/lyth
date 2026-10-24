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
silently skip a test.

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

struct gradeCase {
    Score: int
    Want: string
}

func TestGrade(t: *testing.T) {
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

Subtests are `t.Run` with a trailing lambda. Loop variables are fresh per
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

Flags other than Lyth's own (`-tags`, `-diagnostics`, `-timings`) go to
`go test`. Failures point at `.lyth` lines:

```text
--- FAIL: TestSlug (0.00s)
    slug_test.lyth:10: slug = "hello-lyth--world", want "hello-lyth-world"
```

`lyth build` and `lyth run` ignore test files; `lyth check`, `lyth vet`, and
`lyth gen` include them. External test packages (`package foo_test`) are not
supported yet.
