# ADR 0035: Enums travel by name, and string-backed enums

**Status:** Accepted (2026-10-25). Amends [0025](0025-enums-and-sealed-interfaces.md).
Breaking: the JSON form of every enum changes.

## Context

`enum Status { Active, Suspended }` lowers to `type Status int` with `iota`
constants and a generated `String()` (ADR 0025). That representation is right
inside a program, but it has four problems at the edges, where backend code
lives.

- **Clients can crash an exhaustive `when`.** `encoding/json` encodes the
  number (`"status":1`) and decodes any number. A client sending
  `{"status": 7}` gets a `Status(7)` without error, and the next exhaustive
  `when` panics: `lyth: no when branch for this Status`.
- **Stored data depends on declaration order.** Reordering entries, or
  inserting one in the middle, silently changes what stored and transmitted
  numbers mean.
- **Text columns do not round-trip.** pgx (v5.11) encodes a named integer type
  as an integer. For a text column it falls back to `fmt.Stringer` and writes
  `"Paid"`, then cannot scan `"Paid"` back: `cannot scan text (OID 25) in text
  format into *main.Status`. pgx never consults `encoding.TextMarshaler`.
  Making one enum work with JSON, pgx, and `database/sql` took six hand-written
  methods (about 27 lines) in the review. `String()` is generated and cannot be
  redefined (E0102), so a lower-case wire spelling needs yet another method.
- **No enum can match an existing contract.** An enum cannot match a
  specification that fixes the wire values (`"in_progress"`,
  `"PAYMENT_FAILED"`). Go code uses `type Status string` and constants for this
  (the most common enum shape in Go APIs), which Lyth cannot declare today.

Other languages default to names on the wire: Rust's serde, Kotlin's
serialization and Jackson, Swift's `Codable` with raw values, and protobuf's
JSON mapping.

## Decision

### Plain enums: names on the wire
`enum Status { Active, Suspended, PendingReview }` keeps its Go
representation: `type Status int`, `iota` constants named `StatusActive`, and
`String()` returning the entry name. The zero value is still the first entry.
Lyth additionally generates:

```go
// MarshalText returns the wire name of the Status entry.
func (s Status) MarshalText() ([]byte, error) {
	switch s {
	case StatusActive:
		return []byte("active"), nil
	case StatusSuspended:
		return []byte("suspended"), nil
	case StatusPendingReview:
		return []byte("pendingReview"), nil
	}
	return nil, fmt.Errorf("invalid Status %d", int(s))
}

// UnmarshalText sets s to the entry with the given wire name.
func (s *Status) UnmarshalText(text []byte) error {
	switch string(text) {
	case "active":
		*s = StatusActive
	case "suspended":
		*s = StatusSuspended
	case "pendingReview":
		*s = StatusPendingReview
	default:
		return fmt.Errorf("invalid Status %q", text)
	}
	return nil
}
```

- **Wire name.** The wire name of an entry is its name under the automatic
  `json` key rule ([ADR 0036](0036-automatic-json-tags.md)): `Active` →
  `active`, `PendingReview` → `pendingReview`. JSON keys and enum values then
  share one case convention.
- **Unknown values are rejected.** `UnmarshalText` rejects any other text, so
  decoding untrusted input can no longer produce a value outside the set.
  `when` stays sound, and an invalid request becomes a decode error (400)
  instead of a panic (500).
- **Who uses the methods.** `encoding/json` (v1 and v2), `flag.TextVar`,
  slog's handlers, and most text formats use them. In logs, slog then shows
  `active`, while `fmt` and string templates keep using `String()` (`Active`).
- **Overriding.** An enum may declare `MarshalText`/`UnmarshalText` itself;
  Lyth then generates neither. It declares both or neither (E0216
  otherwise). Text methods always put text on the wire, so the escape hatch
  for keeping numbers in JSON is `MarshalJSON`/`UnmarshalJSON`, which
  `encoding/json` prefers to the generated pair.
- **Wire names are unique.** Two entries whose wire names coincide (`Id` and
  `ID` both give `id`) are E0102: rename one, or declare the pair.
- **Databases.** Plain enums store their number in integer columns, as today.
  Text columns use a string-backed enum.

### String-backed enums: Go's `type T string` with exhaustiveness

```kotlin
enum Plan: string {
    Free = "free",
    Pro = "pro",
    Enterprise = "enterprise",
}
```

- **Lowering.** It lowers to Go's idiom: `type Plan string`, typed constants
  `PlanFree Plan = "free"`, a `String()` that returns the value, and an
  `UnmarshalText` that accepts exactly the declared values.
- **What works.** `encoding/json` encodes the value. pgx and `database/sql`
  write and scan it as a string with no extra methods. Exhaustive `when`, entry
  names inside `when`, `==`, `Plan.Pro`, and `string(p)` work as for plain
  enums.
- **Values.** Each entry needs a value, a string literal (E0216), and the
  values must be distinct (E0102). A plain enum's entry with a value is
  E0601, with a hint naming the string-backed form. `string` is the only backing type; numeric wire codes use
  a defined type and constants ([ADR 0037](0037-type-declarations.md)), which
  are not a closed set.
- **Zero value.** `""` is not an entry unless one is declared with `""`.
  Without one, the type has no zero value in Lyth: a field of this type must be
  given at construction, and `p: Plan` without an initializer is E0405, like a
  non-null pointer (types.md §6). Reflection can still produce `""` (a JSON
  field left out decodes to it), and `when` then panics as for any value
  outside the set. Input types should declare such fields `Plan?`, which
  `omitzero` and validators treat as optional.

## Alternatives

- **Verbatim entry names on the wire (`"PendingReview"`).** This matches
  `String()`, Rust, Kotlin, and protobuf's JSON mapping, and log output stays
  identical to the wire. It is rejected because JSON keys are lower-camel in
  Lyth (ADR 0013, 0022), and PascalCase values next to lower-camel keys look
  foreign in most APIs. This is the one real choice in this ADR; accepting
  verbatim names changes only the wire-name rule above.
- **Make every enum string-backed.** One representation, and database-ready,
  but the zero value is no longer an entry. Every plain enum field would
  become required, and `int(c)`/`Color(n)` conversions disappear.
- **Generate `Scan`/`Value` as well**, so plain enums use text columns. This
  imports `database/sql/driver` into every package with an enum and adds two
  methods each; string-backed enums give the same result through the type
  itself.
- **Keep numbers and validate in `UnmarshalJSON`.** It fixes the crash but
  keeps order-dependent wire values and still cannot match a fixed contract.

## Consequences

- **Wire format.** The JSON form of every plain enum changes from a number to
  a name. Nothing else changes in Lyth code; Go callers see two more methods.
- **Generated code.** Each plain enum gets about 25 more lines of Go. There
  is no run-time cost unless the methods are called, and they cost what
  hand-written ones would.
- **Exhaustiveness at the edge.** Exhaustive `when` becomes safe on decoded
  input, which was the point of exhaustiveness for backend code.
- **Validation.** validator's `required` still cannot tell a missing plain
  enum from its first entry. The guide recommends `Status?` for optional
  input, or a first entry such as `Unknown` when "missing" must be detected.

### Migration
- **No rewrite needed.** No source rewrite is needed: existing enum
  declarations keep compiling. The change is in what they put on the wire, so
  it is a release-note item. The guide's version table gains a row: "Enums
  encode as names in JSON; declare `MarshalText`/`UnmarshalText` to keep
  numbers".
- **New compiler error.** For one release, an enum that already declares only
  one of the pair is an error naming the other.

### Implementation
- **Generated methods.** `enumDecl` in `internal/lower/lower.go` generates the
  methods beside `String()`; `fmt` is imported only when they are generated.
  The checker decides whether to generate them once every method is declared
  (`finishEnums` in `internal/check/decl.go`) and adds them to the shadow
  type, so `encoding.TextMarshaler` and interface checks see them.
- **Parser.** `enum Name: string { Entry = "value", ... }`.
- **Checker.** `resolveEnum` checks values and wire names; a string-backed
  enum without a `""` entry is not zeroable.
- **Tests.** `run/enumwire` (JSON v1 and v2 round trips, rejected numbers and
  names, map keys, slog, `MarshalJSON` keeping numbers),
  `run/enums` (the name in JSON), `gen/enums`, `gen/stringenum`,
  `check/enumwire_neg` (half-declared pair, duplicate values and wire names,
  missing and non-literal values), `check/enumzero_neg`, and
  `examples/todoapi/internal/todo/enumcolumn_test.lyth`, which writes and
  scans a string-backed enum through pgx when `DATABASE_URL` is set.
