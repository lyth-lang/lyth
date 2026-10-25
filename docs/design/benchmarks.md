# Benchmarks: Lyth versus hand-written Go

This document checks whether Lyth's design promise holds: code written in
Lyth should perform like idiomatic Go, because it *is* Go once compiled, with no
runtime and no wrappers. This document reports how we measured that, what we
found, and what we changed because of it. The suite lives in
[`bench/`](../../bench), and the raw results are in
[`bench/results/`](../../bench/results).

## 1. Summary

- **Run time, memory, allocations: no measurable difference.** Across 18
  benchmarks in 7 workloads (error propagation, JSON, CPU-bound parsing,
  allocation-heavy indexing, HTTP handlers and a loopback server, concurrent
  fan-out with `errgroup`, PostgreSQL through `pgx`), benchstat finds no
  significant difference in time per operation, bytes per operation, or
  allocations per operation (allocations are identical in every benchmark).
  Binary size is within 1% (Lyth's is 0.7% smaller), and peak RSS is equal.
- **Builds: a fixed overhead of about 17 ms.** A warm rebuild after a code
  change takes 160 ms with `lyth build` and 143 ms with `go build` (+11.9%). A
  cold build (empty Go build cache) takes 2.11 s and 2.09 s, which is noise.
- **The suite found three problems, all fixed:** a lowering that allocated
  twice as much as the Go idiom (`err.wrap` with a template), a 160 ms build
  cost from `go list` (now served from a persistent cache), and two bugs in the
  benchmark harness itself (§5).

## 2. Workloads

Each workload is a package with two implementations, `goimpl` (Go) and `lythimpl`
(Lyth), written independently and idiomatically in each language (not
transliterated), behind the same exported API. A shared `*_test.go` file runs
the same benchmarks against whichever implementation the build tag `impl_lyth`
selects. `TestEquivalence` checks that both produce identical results on the
same inputs before any timing is trusted.

| Workload | What it exercises | Lyth features involved | Benchmarks |
|---|---|---|---|
| `errprop` | error propagation through 1 and 10 call levels, with and without wrapping | `throws`, `?`, `err.wrap("...${x}")`, `catch` with fallback | `Chain`, `ChainWrapped`, `Fallback` |
| `jsoncodec` | decoding and re-encoding nested order documents with optional fields | structs with automatic `json` tags, `T?` fields as `*T`, `encoding/json` | `DecodeTotal`, `RoundTrip` |
| `logagg` | CPU-bound log parsing and per-endpoint aggregation (also a standalone program, used for build and RSS metrics) | `strings`, `slices`, maps of structs, string templates | `Aggregate` |
| `index` | building an inverted index (allocation-heavy maps and slices) | `map[string][]int`, `append`, loops | `BuildIndex`, `Summaries` |
| `httpapi` | an HTTP handler through `httptest`, and a loopback server with a latency distribution (p50/p90/p99) | `net/http` interop, `catch` with early return, JSON request and response bodies | `Handler`, `ServerLatency` |
| `fanout` | concurrent hashing with cancellation on the first error | `golang.org/x/sync/errgroup`, closures, `sync/atomic`, `context` | `ProcessAll` |
| `pgapi` | fetching a row from PostgreSQL and encoding it as JSON | `pgx/v5` and `pgxpool` interop, `errors.Is` | `FetchJSON` |

The Lyth implementations are 3–37% shorter than the Go ones (277 versus 344
lines in total), mostly because `?` replaces `if err != nil` blocks.

`collections` was added after the results in §4 and measures something
different: the Lyth side uses `lyth/coll` chains and the Go side the loops a Go
programmer writes instead, at 100 to 100,000 elements. It shows what a chain
costs relative to a loop, not a difference in generated code, so it is
reported separately ([Collection API §5](collections.md#5-mutation-ordering-and-cost),
`bench/results/collections.txt`) and is not part of the summary in §1. A
full `benchrun` run includes it. Its Lyth side is 22 lines and its Go side 49.

## 3. Method

- **Driver.** `go run ./cmd/benchrun` builds `lyth` from the repository, runs
  `lyth check` and the equivalence tests, compiles one test binary per package
  and implementation (`go test -c`), then runs the binaries in **alternating
  rounds** (Go, Lyth, Go, Lyth, …; 10 rounds of `-benchtime 500ms`) so that
  drifting machine state affects both sides alike.
- **Statistics.** `benchstat` (golang.org/x/perf, pinned version) compares
  the two sets with `-row .fullname -col .file`. Our decision rule counts a
  difference as real only if p < 0.05 *and* it exceeds 2%.
- **Build metrics.** These use the `logagg` program in both languages. A cold
  build uses a fresh, empty `GOCACHE`. A warm rebuild appends a new function to
  the main source file (so the generated Go changes too) and rebuilds with the
  same cache. The Go, Lyth, and Lyth-without-type-cache series alternate within
  each of 5 runs; we report medians.
- **Run time and RSS.** Each `logagg` binary processes 1 million generated
  log lines 5 times. Peak RSS comes from `getrusage`.
- **Environment.** Apple M1 Pro (10 cores), macOS, Go 1.26.7, PostgreSQL 14
  on a local Unix socket, on a developer laptop that was otherwise idle.

## 4. Results

From `bench/results/benchstat.txt` (final run of 2026-10-24, with the
compiler as of the first backend example). Times are benchstat medians; `~` means no
significant difference.

| Benchmark | Go | Lyth | Verdict |
|---|---:|---:|---|
| errprop Chain depth=1 (ok / fail) | 4.21 ns / 4.19 ns | 4.20 ns / 4.20 ns | ~ / ~ |
| errprop Chain depth=10 (ok / fail) | 15.82 ns / 12.96 ns | 15.81 ns / 12.91 ns | ~ / ~ |
| errprop ChainWrapped depth=10 (ok / fail) | 15.83 ns / 1.264 µs | 15.84 ns / 1.237 µs | ~ / ~ |
| errprop Fallback | 12.60 ns | 12.59 ns | ~ |
| fanout ProcessAll (ok / fail early) | 1.593 ms / 1.098 ms | 1.613 ms / 1.137 ms | ~ / ~ |
| httpapi Handler | 15.82 µs | 16.07 µs | ~ |
| httpapi ServerLatency (mean) | 76.0 µs | 79.8 µs | ~ |
| index BuildIndex / Summaries | 900 µs / 393 µs | 897 µs / 399 µs | ~ / ~ |
| jsoncodec DecodeTotal 5 / 50 items | 4.88 µs / 28.16 µs | 4.80 µs / 28.23 µs | ~ / ~ |
| jsoncodec RoundTrip | 32.84 µs | 33.03 µs | ~ |
| logagg Aggregate | 982 µs | 1.003 ms | ~ |
| pgapi FetchJSON | 24.7 µs | 25.9 µs | ~ |

- **Allocations** are equal in all 18 benchmarks (for example 952 B and 20
  allocations for a failing 10-level wrapped chain, 71 allocations per HTTP
  request, 13 per PostgreSQL fetch). Benchstat flags no byte-level difference
  in the final run; earlier runs flagged 0.01% (`ProcessAll/fail-early`) and
  0.4% (`ServerLatency`), far below the 2% threshold and caused by goroutine
  and network scheduling, not by generated code.
- **Latency** of the loopback server: p50 69.5 µs versus 68.5 µs, p99 154 µs
  versus 162 µs (~ in both cases).
- **Stability.** In this run some intervals are wide (up to ±94% on one
  error-path benchmark, ±30% elsewhere), so medians matter; they agree within
  1–5%, and no difference is significant. An earlier complete run without the PostgreSQL workload
  ([`benchstat-run2.txt`](../../bench/results/benchstat-run2.txt)) had
  intervals of ±0–3% and reached the same verdict on every benchmark.

| Build and binary metric | Go | Lyth | Δ |
|---|---:|---:|---:|
| binary size | 2505.9 KiB | 2488.5 KiB | −0.7% |
| cold build (empty GOCACHE) | 2088 ms | 2106 ms | +0.9% (noise) |
| warm rebuild after a code change | 143 ms | 160 ms | +11.9% |
| warm rebuild, Lyth type cache disabled | 143 ms | 246 ms | +72% |
| `logagg` run time, 1M lines | 246 ms | 247 ms | +0.4% |
| `logagg` peak RSS | 124.5 MiB | 124.5 MiB | 0.0% |

Breakdown of a warm `lyth build` with nothing to recompile (`-timings`): setup
10.7 ms (module discovery and one `go env` call), parse 0.3 ms, Go type loading
2.1 ms (from the cache), check 0.3 ms, lowering and emission 1.3 ms, validation
0.3 ms, write 1.3 ms, `go build` 57.3 ms. Lyth's own work is about 16 ms; the rest
is the Go toolchain.

## 5. What the benchmarks changed

1. **`err.wrap` with a template allocated twice as much as Go.** The first
   version lowered `err.wrap("level ${n}")` to string concatenation followed by
   `fmt.Errorf("%s: %w", msg, err)`: two allocations where Go code makes one
   `fmt.Errorf("level %d: %w", n, err)`. The lowering now turns template parts
   into format verbs (`call.go`, `wrapTemplate`). A failing `ChainWrapped` call
   now costs 952 B and 20 allocations, exactly like Go. This is the only case
   where generated code was measurably worse, and the decision rule caught it.
2. **Builds spent most of their time in `go list`.** Loading Go dependency
   types through `go list -export` took 80–220 ms per build, more than all of
   Lyth's other phases together. A persistent cache of export-data locations
   ([compiler.md §8](compiler.md#8-caching-and-incremental-compilation)), keyed
   on the toolchain, module graph, build tags, and local package fingerprints,
   brings type loading to about 2 ms. The warm-rebuild overhead dropped from
   +72–78% to +12–15%. Merging the toolchain check's `go env` call into the cache
   key saved another process start.
3. **Two harness bugs** made the first runs misleading:
   - benchstat grouped rows by `.name`, which merges sub-benchmarks
     (`Chain/depth=1` with `Chain/depth=10`). Intervals widened to as much as
     ±160%, so every verdict was `~` by construction. Rows are now keyed by
     `.fullname`.
   - The warm rebuild appended a *comment*. Lyth does not emit comments, so the
     generated Go did not change, `go build` had nothing to do, and Lyth looked
     51% *faster* than Go. The edit now adds a function in both languages.

## 6. Interpretation and limits

The result follows from how Lyth is built. Every construct lowers to the Go a
careful programmer would write (`?` to `if err != nil { return …, err }`, `T?`
to `*T`, templates to `+` and `strconv`, `when` to `switch`), and the Go
compiler optimizes that code the same way it optimizes hand-written code. The
benchmarks guard against regressions in lowering, not against a runtime
penalty, because there is none.

Threats to validity:

- One machine and OS (a laptop, not dedicated hardware). Absolute numbers will
  differ elsewhere; the Go/Lyth ratio is what we compare.
- One author wrote both implementations. Equivalence tests make sure both
  compute the same thing, but neither side was tuned beyond idiomatic code.
- The workloads cover common backend code. Generics-heavy code, enums and
  sealed interfaces, and `select` have no benchmark pairs yet.
- PostgreSQL runs locally, so `pgapi` measures client-side cost plus a local
  round trip, not network latency.

## 7. Reproducing

```bash
cd bench
createdb -h /tmp lyth_bench   # once; the PostgreSQL workload is optional
LYTH_BENCH_DATABASE_URL=postgres:///lyth_bench go run ./cmd/benchrun -count 10 -benchtime 500ms -build-runs 5
```

The driver writes `results/go.txt`, `results/lyth.txt`, `results/benchstat.txt`,
and `results/report.md`. Without `LYTH_BENCH_DATABASE_URL` the PostgreSQL
workload is skipped.
