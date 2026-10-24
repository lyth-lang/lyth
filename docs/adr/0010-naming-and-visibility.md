# ADR 0010: Verbatim Go names; `pub` and capitalization

**Status:** Superseded by [0022](0022-capitalization-is-visibility.md)

## Context
Lyth wants camelCase functions and PascalCase types. Go encodes export in
capitalization, and Go APIs use names like `URL`, `ServeHTTP`, `IPv4`.

## Decision
- Go identifiers are referenced verbatim from Lyth (`http.ListenAndServe`, `r.URL`).
- Lyth declarations use Lyth conventions. Visibility is explicit: top-level items
  are private unless `pub`, struct fields are public unless `private`, interface
  methods are public. The Go name is the Lyth name with its first letter
  upper-cased (public) or lower-cased (private), plus `_` for private names that
  hit Go keywords or predeclared identifiers. Collisions are errors.
- Methods implementing Go interfaces use the Go method name (`pub fun T.Error()`),
  so there is one spelling.

## Alternatives
- **Lower-case aliases for Go APIs** (`http.listenAndServe`): needs heuristics for
  initialisms (`URL` → `uRL`?), produces collisions (`Url` vs `URL`), gives two
  spellings per entity, and breaks copy-paste from Go docs. Rejected.
- **Go's capitalization rule as Lyth's visibility rule**: forces PascalCase
  exported functions in Lyth source, contradicting the naming preference.
- **Initialism-aware Go names** (`userId` → `UserID`): heuristic. Deferred as an
  open question.

## Consequences
Lyth code visibly distinguishes Go calls (PascalCase) from Lyth calls (camelCase).
Some find that useful provenance, some find it inconsistent. The mapping is
total, deterministic, and documented.
