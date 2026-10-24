# ADR 0013: Public fields by default; automatic `json` tags

**Status:** Accepted; field visibility and the json key rule amended by [0022](0022-capitalization-is-visibility.md)

## Context
Go's reflection-based libraries (`encoding/json`, `database/sql` scanners,
templates, validators) only see exported fields. A private-by-default field
policy would make every Lyth data struct serialize as `{}`, Go's most common
beginner trap. Also, Go's JSON keys default to the Go field name (`Name`),
while Lyth developers wrote `name`.

## Decision
- Struct fields are public (Go-exported) unless marked `private`. Top-level
  declarations stay private by default.
- For public fields whose tag has no `json` key, Lyth emits `json:"<lythName>"`.
  An explicit `json:"..."` (including `json:"-"`) is used verbatim.

## Alternatives
- **Fields follow struct visibility**: private structs still need exported Go
  fields for reflection, so this does not solve the problem.
- **Private by default (Rust)**: the JSON trap.
- **No automatic tags**: responses show `"Name"`/`"CreatedAt"` keys, which
  surprises everyone who did not write Go.

## Consequences
JSON keys match Lyth source names. Other tag-driven libraries (yaml, db) still
need explicit tags. Changing the auto-tag rule later would break wire formats,
so the rule is frozen.
