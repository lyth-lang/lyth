# ADR 0026: Null converts to a nil interface

**Status:** Accepted (completes 0009; leaves 0017 unchanged)

## Context
Go converts a nil pointer to a non-nil interface that holds a nil pointer.
`var p *MyErr = nil; var err error = p` makes `err != nil` true, the typed-nil
trap behind many Go bugs: a function that returns a concrete error pointer as
an `error` reports failure on success. Lyth lowered conversions the same way,
which broke its own null safety. In

```kotlin
func check(e: error?): string = if e == null { "ok" } else { e.Error() }
check(find())   // find returns *MyErr?, here null
```

the checker accepts `e.Error()` because `e == null` was tested, and the
program panicked with a nil dereference, since the test was false. Optional
values had the same problem in another form: an `Int?` converted to `Any?`
held an `*int`, so `v is Int` was false for a present value.

## Decision
- When a nullable value becomes a nullable interface type in Lyth code
  (`Error?`, `Any?`, a Lyth or Go interface), null becomes a nil interface.
  This covers optional values, nullable pointers, functions, and channels, and
  Go values of unknown nullability (`*T!`). Interface values and non-null
  values convert as they are; slices and maps are never null.
- An optional value goes in unboxed: a present `Int?` becomes an `Int` in the
  interface, so type tests and Go code see the value, not a pointer.
- The conversion lowers to one nil check where it happens:
  `var e error; if p != nil { e = p }`, with the declared variable as the
  target, and `if p == nil { return nil }; return p` for a returned value.
- Go parameters keep ADR 0017's rule: a nullable value goes in its Go
  representation (`*T`, nil when null), because drivers and encoders read a
  nil `*T` as NULL and Go code passes exactly that.
- A tuple assignment from one call (`(err, n) = f()`) cannot check its values
  in between, so such a conversion there is an error with a note to
  destructure into new variables first.

## Alternatives
- **Keep Go's conversion**: zero cost, but null checks in Lyth code would
  depend on whether a value had passed through an interface, which is not
  null safety.
- **Reject the conversion and require `!!` or a check**: sound, but the
  common case (returning a concrete error pointer as `Error?`) would need a
  manual `if` every time, the very boilerplate Lyth removes.
- **Apply the rule to Go parameters too**: consistent, but `pgx`,
  `database/sql`, and `encoding/json` would receive `nil` interfaces instead
  of typed `*T` values, which changes what they do (ADR 0017).

## Consequences
The cost is a nil check and a few lines of Go at conversions from nullable
values to interfaces, which are rare in Lyth code: after a null check the
value is non-null and converts directly. Calls to Go APIs are unchanged, so
`fmt.Sprint(x)` for a null `Int?` still receives a nil `*int` and prints
`<nil>`, as in Go.
