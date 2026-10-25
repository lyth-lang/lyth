# Collection API (`lyth/coll`)

**Status:** the operations marked v1 are implemented and tested
(`tests/testdata/run/coll.lyth`, `run/collsemantics.lyth`); the rest of this
document is proposed.

`lyth/coll` is the standard library's package of collection operations
([ADR 0030](../adr/0030-standard-library.md)). Each operation is a generic
extension function ([ADR 0029](../adr/0029-extension-functions.md)): Lyth
code writes `todos.Filter { !it.Done }`, and the generated Go is the call
`coll.Filter(todos, func(it Todo) bool { return !it.Done })`. This document
fixes the package's conventions, its first version, and what it will not do.

## 1. Goals and non-goals

Goals:

- Read data transformations top to bottom, as a chain, instead of as nested
  calls or a loop whose purpose the reader has to reconstruct.
- One convention for every operation: the receiver is never changed, the
  result is new, the order is kept, an empty input gives an empty result.
- Stay learnable for Go programmers: the operations are Go functions with Go
  names, visible in the generated code, and Go's `slices` and `maps`
  packages stay usable beside them.
- State the cost of each operation, so that a reader can decide when a loop
  is the better choice.

Non-goals:

- Lazy sequences, streams, or query objects (§7).
- Translating chains to SQL or to any other query language. A chain works on
  values already in memory (§6).
- A replacement for loops. A loop remains the right choice for hot paths, for
  work that can fail, and for anything that mutates in place.
- A new collection hierarchy. The receivers are Go slices and maps.

## 2. The API

### Naming

The operations are named as exported Go functions: `Filter`, not `filter`.
Capitalization is visibility in Lyth (ADR 0022), so a lower-case extension
would be private to `lyth/coll`, and a Lyth name is its Go name, so Go code
that calls the package sees the same names. Lower-case built-in methods
exist only where the compiler lowers a call to a Go operator or to a single
standard-library call (`s.toInt()`, `ch.send(v)`); collection operations are
ordinary library code that users can read and imitate (ADR 0030 rejected
compiler intrinsics for them).

Within that rule, the vocabulary is the common one (Kotlin, C#, JavaScript)
where its meaning matches, and Go's where Go already has the operation:

- A name says what is returned. `SortedBy` returns a sorted copy; Go's
  `slices.SortFunc` sorts in place, and keeping the past participle makes
  the difference visible in the call.
- Membership is Lyth's `in` operator (`id in ids`, `id !in blocked`), so
  there is no `Contains`.
- One function per operation. `Filter` with a negated condition replaces
  `Exclude`/`FilterNot`; `Find` replaces `First`/`FirstOrNull`.

### Operations on slices

| Operation | Result | Constraint | v1 |
|---|---|---|---|
| `xs.Filter { cond }` | the elements for which `cond` is true | | ✅ |
| `xs.Map { f(it) }` | `f` of each element | | ✅ |
| `xs.FlatMap { list(it) }` | the results of each call, concatenated | | ✅ |
| `xs.Find { cond }` | `(element, true)` for the first match, else `(zero, false)` | | ✅ |
| `xs.Any { cond }`, `xs.All { cond }` | whether some / every element matches (`All` is true for an empty slice) | | ✅ |
| `xs.Count { cond }` | the number of matches | | ✅ |
| `xs.Fold(initial) { acc, x -> ... }` | the accumulated value; `initial` for an empty slice | | ✅ |
| `xs.Partition { cond }` | `(matching, rest)` | | ✅ |
| `xs.Distinct()` | the elements without repeats, first occurrence kept | `T: comparable` | ✅ |
| `xs.DistinctBy { key(it) }` | the first element for each key | `K: comparable` | ✅ |
| `xs.SortedBy { key(it) }` | a copy, stably sorted by the key, ascending; the key runs once per element | `K: cmp.Ordered` | ✅ |
| `xs.SortedWith { a, b -> cmp.Compare(...) }` | a copy, stably sorted by a comparison (several keys, descending); the comparison runs once per comparison | | ✅ |
| `xs.Take(n)` | a copy of the first `n` elements, or all of them; empty for `n <= 0` | | ✅ |
| `xs.GroupBy { key(it) }` | `map[K][]T`, each group in input order | `K: comparable` | ✅ |
| `xs.AssociateBy { key(it) }` | `map[K]T`; a later element replaces an earlier one with the same key | `K: comparable` | ✅ |
| `xs.ToSet()` | `map[T]bool` holding every element, for `x in set` lookups | `T: comparable` | ✅ |

### Operations on maps

| Operation | Result | v1 |
|---|---|---|
| `m.Keys()`, `m.Values()` | the keys or values as a slice, in Go's unspecified map order | ✅ |
| `m.SortedKeys()` | the keys in ascending order (`K: cmp.Ordered`) | ✅ |

A package can declare only one extension of a name (ADR 0029), so a map
operation that resembles a slice operation needs its own name
(`FilterValues`, not a second `Filter`). None is in v1 (§8).

## 3. Examples

Ranking, with a set for membership:

```kotlin
blocked := blockedIDs.ToSet()
result := profiles
    .Filter { it.ID !in blocked && !it.Hidden }
    .SortedBy { it.Distance }
    .Take(limit)
```

`blocked` is a map, so each test is a lookup. With a slice
(`it.ID !in blockedIDs`) the same chain scans `blockedIDs` once per profile.

Several sort keys, descending:

```kotlin
leaders := players.SortedWith { a, b ->
    when {
        a.Score != b.Score -> cmp.Compare(b.Score, a.Score)
        else -> strings.Compare(a.Name, b.Name)
    }
}
```

Grouping, then reading the groups in a stable order:

```kotlin
byTag := todos.GroupBy { it.Tag }
for tag in byTag.SortedKeys() {
    println(tag, byTag[tag].Map { it.Title })
}
```

Deduplication by a key, and nested data:

```kotlin
latest := events.SortedWith { a, b -> b.At.Compare(a.At) }.DistinctBy { it.UserID }
tags := posts.FlatMap { it.Tags }.Distinct()
```

Lookup with Go's comma-ok form:

```kotlin
admin, found := users.Find { it.Role == Role.Admin }
if !found {
    return null
}
```

## 4. Types and empty results

- **Receivers.** Slices `[]T` (an array is sliced first: `arr[:]`) and maps
  `map[K]V`. Nullable receivers do not exist: slices and maps are never
  nullable in Lyth, and a nil slice or map is simply empty.
- **Inference.** `T`, `K`, and `V` come from the receiver; a result type
  (`R` of `Map`, `K` of `GroupBy`) comes from the lambda's result. Explicit
  type arguments are never needed in ordinary calls.
- **Empty input.** A nil or empty receiver gives an empty result: an empty,
  non-nil slice (`[]` in JSON, never `null`), an empty map, `false` from
  `Any`, `true` from `All`, `0` from `Count`, `initial` from `Fold`, and
  `(zero, false)` from `Find`.
- **`Find` returns `(T, bool)`, not `T?`.** Nullable type parameters are not
  supported yet, and the comma-ok pair is what a Go reader expects. When `T?`
  works for type parameters, a nullable form can be added (§8).
- **Callbacks** run at most once per element, in order, on the calling
  goroutine. `Find`, `Any`, and `All` stop at the first element that decides
  the result; every other operation calls its callback once for each
  element, and `SortedBy` computes all keys before sorting. The exception is
  `SortedWith`, whose comparison runs for each comparison the sort makes, in
  an unspecified order. Callbacks cannot fail: a lambda passed to these
  functions is not `throws`. Work that can fail is a loop with `?`, which
  keeps the error path visible.

## 5. Mutation, ordering, and cost

- **No operation changes its receiver.** Every slice result has its own
  backing array, so appending to it or assigning its elements never affects
  the input. Elements are copied as values: a pointer element still points
  at the same struct.
- **Order.** Results keep the input order; both sorts are stable; `GroupBy`
  keeps input order within each group. Map iteration order is Go's
  (unspecified): use `SortedKeys` when order matters.
- **Eager evaluation.** Each operation runs when it is called and allocates
  its result, so a chain of three operations builds three slices. There is no
  hidden fusion or laziness.

### Measurements

`bench/collections` compares three Lyth chains with the loops a Go programmer
writes instead (`goimpl`), at 100 to 100,000 elements. Both sides meet the
same output requirements, which `TestEquivalence` checks: the same results,
the input left alone, and new, right-sized, non-nil results (the Go ranking
copies its top 20 out of the filtered buffer rather than returning a view of
it). Results for an Apple M1 Pro, Go 1.26.7, six runs per side alternating
which goes first, compared with benchstat (`bench/results/collections.txt`):

| Workload | n = 100 | n = 1,000 | n = 10,000 | n = 100,000 |
|---|---|---|---|---|
| `Filter` (89% kept) → `SortedBy` → `Take(20)` | +26% time, 3.8× memory | +13%, 5.1× | +7%, 5.4× | +6%, 6.7× |
| `Filter` (50% kept) → `Map` | +134%, 3.6× | +114%, 3.5× | +125%, 3.7× | +133%, 3.7× |
| `SortedBy` alone | +15%, 2.2× | +3%, 2.3× | no significant difference, 2.3× | no significant difference, 2.3× |

The memory comes from three places, all visible in `std/coll/coll.lyth`:
`Filter` grows its result by doubling (14–30 allocations where the Go loop
preallocates once), `SortedBy` sorts (key, element) pairs and then copies the
elements out (two arrays of the input's length), and each step of a chain
allocates its own result. All of it is garbage once the call returns.

How to read them:

- **Sorting dominates when it is present.** The ranking chain's extra cost
  falls from 26% to 6% as the sort's O(n log n) work grows; the chain
  allocates more (the filtered copy grows by doubling, the sort works on a
  copy with its keys), but that memory is garbage after the call.
- **A cheap filter and map pay for the intermediate slice.** `Filter` copies
  whole records into a new slice before `Map` reads one field of each; the
  loop appends only the field. This is the cost of eager chains, and for a
  hot path over large slices a loop is the better tool.
- **A first measurement over-stated and under-explained these costs.** It
  used one size, compared against a loop that sorted in place and returned a
  sub-slice of a larger buffer (not the same result), and ran outside the
  suite without significance testing.

Changes made from these measurements:

- `SortedBy` computes each key once, then sorts (key, element) pairs. It
  called the key twice per comparison, about 2·n·log₂ n times, which also
  contradicted the callback rule above. At 10,000 records the previous
  version took 1.82–2.57 ms and the new one 1.39–1.49 ms (three scratch
  runs each); in the suite the new one shows no significant difference from
  a sorted clone in Go from 1,000 elements on, for a temporary buffer of the
  pairs.
- **Not changed: `Filter` keeps growing its result by appending.**
  In a scratch comparison (10,000 records, three runs, not in the suite),
  preallocating the input's length was 1.6–3.9× faster when most elements
  match, but 1.6× slower and 40× the memory when 1% match,
  and it keeps a full-size buffer alive behind a small result for as long as
  the result lives. Neither choice wins everywhere; growing keeps memory
  proportional to the result.
- Nothing is lazy or fused. A fused `Filter`-then-`Map` would need a second
  name for it or a compiler that rewrites chains; the measurements show the
  cost only for large, cheap pipelines, where a loop is clearer anyway.

## 6. Database boundaries

`lyth/coll` works on values that are already in memory. It does not build
queries, and nothing in the compiler translates a chain into SQL. Filtering,
sorting, pagination, and aggregation of stored data belong in the query, as
in `examples/todoapi`, whose store takes a filter and a limit and returns one
page. Loading a table to call `Filter` and `Take` on it moves the work to the
wrong place and the data over the network. Collection operations fit the
data after the query: shaping a response, grouping a page of results,
joining two small result sets with `AssociateBy`.

## 7. Rejected alternatives

| Alternative | Why not | Instead |
|---|---|---|
| Lower-case built-in methods (`xs.filter`) | compiler intrinsics instead of readable library code; lower-case names are private in Lyth; Go callers would see different names | exported extensions in `lyth/coll` |
| Importing `lyth/coll` implicitly | the generated Go imports `lyth/coll`, which needs the toolchain to build; an explicit import keeps that dependency visible | the import, and a diagnostic that names it when it is missing |
| Lazy sequences or streams | less predictable (when does the callback run, how often) and harder to debug | eager operations; `for x in seq` for Go iterators |
| `Exclude` / `FilterNot` | a second name for `Filter` with `!` | `Filter { !cond }` |
| `First` / `FirstOrNull` | `T?` does not work for type parameters yet; `First()` without a condition is `xs[0]` | `Find`, comma-ok |
| `Contains` | duplicates the `in` operator | `x in xs`, `x in xs.ToSet()` for repeated tests |
| `Unique` | `Distinct` is the established name for the same operation | `Distinct`, `DistinctBy` |
| Mutating `SortBy` | Go already has it | `slices.SortFunc`, `slices.SortStableFunc` |
| `Take` returning a sub-slice | it would alias the input, unlike every other operation | a copy; `xs[:min(n, len(xs))]` when aliasing is wanted |
| Operations on arrays and nullable slices | arrays are values that Lyth slices explicitly; slices are never nullable | `arr[:]` |

## 8. Implementation plan

- **v1 (done).** `DistinctBy`, `SortedWith`, `Take`, and `ToSet` added;
  `SortedBy` on a nil slice now returns an empty slice, and computes each
  key once. Diagnostics: a
  call of an operation without `import "lyth/coll"`, or with a Kotlin name
  (`filter`, `first`, `unique`, `sortBy`, ...), names the import and the
  operation; `contains` points to `in`, `exclude` to `Filter { !... }`.
  Tests: `run/coll` (every operation, empty and nil inputs, JSON `[]`),
  `run/collsemantics` (order and stability, input immutability, aliasing,
  shallow copies, `Find` with a zero element, key-call counts, empty and nil
  results), `check/coll_neg` (the hints), `TestCollNames`, which keeps the
  hint table equal to the package's exported functions, and
  `bench/collections` with its equivalence test.
- **v2, when nullable type parameters work.** A nullable `Find` variant,
  decided together with how `T?` lowers for type parameters.
- **v2, from use.** Map operations (`FilterValues`, `MapValues`) only when
  examples need them; each with a `run/` test and a row in §2.
- **Performance, if real programs need it.** `inline func`:
  the compiler inlines a function and its lambda at the call site, which
  removes the closure calls but not the intermediate slices. Measure with a
  benchmark in `bench/` before and after.
- **Lazy variants.** Revisit iterator-based variants that compose with Go's
  `iter.Seq`, which `for` already consumes.
