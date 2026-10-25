# ADR 0040: Optional values passed to Go `any` parameters are unboxed

**Status:** Accepted (2026-10-25). Amends [0017](0017-nullable-values-into-go.md);
extends [0026](0026-null-is-a-nil-interface.md) to Go parameters.
Breaking: what Go code receives for a `T?` passed as `any` changes.

## Context

A value optional `T?` is a `*T` box in Go. When one is passed to a Go
parameter, ADR 0017 passes the box. The reason given: drivers and encoders
read a nil `*T` as NULL, and "would receive a different type than Go code
passes".

For parameters of interface type (`any`, `...any`) the box is visible as a
pointer, and the most common consumers print it:

```kotlin
log.Info("signup", "email", u.Email, "age", u.Age)   // email=0xc47e79a2110 age=0xc47e7996020
fmt.Printf("email=%v\n", u.Email)                     // email=0xc47e79a2110
t.Errorf("discount = %d, want 20", it.DiscountPct)    // discount = 71263924584584 (vet is silent)
```

- **Two disagreeing outputs.** String templates and `println` print the value
  or `null`, so Lyth has two outputs for the same value.
- **The box can be written.** A Go function can write through the box
  (`Scan(edited.Note)` meant as `Scan(&edited.Note)`), which changes every copy
  that shares it. The spec says sharing an optional's box is unobservable
  (types.md §5.6).

ADR 0017's concern was checked against PostgreSQL 14 in the review (pgx v5.11
and `database/sql` with the pgx driver):

| Argument | Stored |
|---|---|
| `(*string)(nil)` | NULL |
| `nil` | NULL |
| `&v` | `v` |
| `v` | `v` |

`encoding/json` encodes both forms identically too. So passing the unboxed
value changes nothing for drivers and encoders, and fixes the printers.

## Decision

- **Interface-typed parameters get the value.** When a nullable Lyth value is
  passed to a Go parameter whose type is an interface (`any`, `...any`,
  `fmt.Stringer`, …), it converts as it already does for Lyth interfaces (ADR
  0026):
  - null becomes a nil interface;
  - a present optional value goes in unboxed (`string`, `int64`, `time.Time`);
  - a present nullable pointer, function, or channel goes in as itself.
- **Pointer-typed parameters are unchanged.** A nullable pointer
  (`*http.Request?`) passed to a Go parameter of that pointer type goes in as
  itself, nil included, as before. An optional value (`string?`) was never
  accepted by a `*string` parameter (E0201), and still is not: a Go API that
  writes through a pointer takes `&x`.
- **Variadic arguments convert one by one.** In `...any`, each argument
  converts on its own, so a slog or `fmt` call with several optionals costs one
  nil check per optional.

## Alternatives

- **Keep ADR 0017 and warn when an optional reaches `fmt`, `log`, `slog`, or
  `testing`.** No behaviour change. But the list is open-ended (testify,
  zap, zerolog, templates), and the warning teaches a workaround
  (`u.Email ?: ""`) for what should just work.
- **Keep ADR 0017 (status quo).** Go code passes `*T` too. But Lyth's `T?` is
  a value, not a pointer to share, and Go code that passes `*string` to `%v`
  is a bug there as well.
- **Unbox for every Go parameter, pointer-typed included.** This breaks every
  API that takes `*T` on purpose.

## Consequences

- **What changes.** Logs, `fmt` output, and test messages print values. Drivers
  and encoders behave the same. Go code can no longer write into an optional's
  box through an `any` parameter: `Scan(edited.Note)` now fails at run time
  (the driver rejects a destination that is not a pointer) instead of
  corrupting shared values.
- **Cost.** Each such argument costs one nil check in the generated Go:
  `var a any; if x != nil { a = *x }`. Converting a non-pointer value to an
  interface may allocate where the pointer did not; on hot logging paths, slog
  attributes (`slog.String`) avoid both.
- **What may break.** Go code that type-asserts its `any` argument to `*T`
  (`v.(*string)`) sees the value instead. That is rare, and the release note
  names it.

### Migration
No source change.
- **Release note.** It is a run-time change at the Go boundary, listed in the
  release notes and the guide's version table.
- **No compile-time signal.** `lyth check` cannot detect Go code that relies on
  receiving a pointer.

### Implementation
- **Lowering.** The conversion helpers of ADR 0026 (`internal/lower`) apply to
  interface-typed Go parameters, Go struct fields of interface type, and the
  arguments of `...any`. The ADR 0017 path remains for nullable pointers
  passed to pointer-typed ones.
- **Tests.**
  - `run/optionals_any`: slog, `fmt`, and `json` with present and null
    optionals, the dynamic type Go receives (`%T`) for values, nil and set
    pointers, call results, and a Go struct field of type `any`.
  - pgx tests in `examples/todoapi` with nullable parameters.
  - `gen/nullable`.
