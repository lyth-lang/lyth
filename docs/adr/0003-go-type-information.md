# ADR 0003: Go type information via `go/packages` export data

**Status:** Accepted

## Context
Lyth's checker must know the exact types of imported Go packages, with modules,
build tags, cgo, vendoring, and workspaces handled the way the `go` command
handles them.

## Decision
Load imported Go packages with `golang.org/x/tools/go/packages` in export-data
mode (`NeedName|NeedTypes|NeedImports|NeedDeps|NeedTypesSizes`), from the module
root, with the user's environment. Lift `go/types` types into Lyth types without
simplification (types.md §1).

## Alternatives
See interop.md §2: generate-only (A), hand-written bindings (B), type-check Go
source ourselves (C), raw `go list -export` + `go/importer` (D), helper packages
(F).

## Consequences
Types are exactly what the compiler checks against. Measured cost: ~0.4 s warm
for `net/http`, `encoding/json`, `database/sql`, and `context`, and the step
fills the build cache the following `go build` reuses. Risk: export-data format
skew when the user's Go is newer than `lyth`'s x/tools. Mitigated by a version
check and a planned source-importer fallback.
