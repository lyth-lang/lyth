# ADR 0014: Tests are Go tests (`lyth test`)

**Status:** Accepted

## Context
A backend language needs unit and integration tests from day one. Go already
has a complete testing toolchain: `go test`, `testing.T`, benchmarks, fuzzing,
coverage, `-race`, `httptest`, and the ecosystem of assertion libraries and CI
integrations built on it. Lyth code compiles to Go packages, so Go tests can
test it.

## Decision
- Test code lives in `_test.lyth` files beside the code under test, in the same
  package, and compiles to `x.lyth_test.go`. `lyth test [packages] [go test
  flags]` compiles the packages with their tests and runs `go test` with the
  overlay. Its flags are go test's (`-v`, `-run`, `-race`, `-cover`, `-count`),
  plus Lyth's `-tags`, `-diagnostics`, `-timings`. `lyth build` and `lyth run`
  ignore test files; `lyth check`, `lyth vet`, and `lyth gen` include them, as
  `go vet` does.
- In a test file, functions named the way go test expects (`TestXxx`,
  `BenchmarkXxx`, `FuzzXxx`, `ExampleXxx`, `TestMain`) keep their Lyth names as
  Go names, without `pub`. Forgetting `pub` would otherwise skip the test
  silently. Their signatures are checked (`fun TestX(t: *testing.T)`).
- Tests, benchmarks, and fuzz targets may be declared `throws`. A generated
  wrapper calls `t.Fatal(err)` when the body throws, so tests can use `?`.
- Hand-written `_test.go` files may sit beside `.lyth` files. They test the
  Lyth package through its Go API, which shows that Lyth packages are ordinary Go
  packages.
- External test packages (`package foo_test`) in Lyth are not supported yet;
  they are reported as such.

## Alternatives
- **A Lyth test framework** (`test("adds") { ... }`, built-in assertions):
  nicer syntax, but it would duplicate `testing`, break `go test -run`, and need
  its own runner, coverage, and IDE integration. Libraries can add assertion
  helpers later without changing the model.
- **Require `pub` on test functions**: consistent with the naming rule, but
  error-prone: a forgotten `pub` turns a test into an unexported function that
  never runs.
- **Compile test files in every build**: simpler, but every build would load
  `testing`/`httptest`, and a broken test would break `lyth build`, unlike Go.

## Consequences
Everything that works with `go test` works with `lyth test`: `-race`, coverage,
CI reporters, `t.Run` subtests. Failures report `.lyth` lines through `//line`
directives (`api_test.lyth:42: status 500, want 201`). A failure inside a
throwing test reports the test's declaration line, because Go errors carry no
position. External test packages need a second package per directory in the
build model; they are planned.
