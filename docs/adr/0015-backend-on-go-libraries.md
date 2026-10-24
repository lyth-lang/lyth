# ADR 0015: Backend services use Go libraries directly

**Status:** Accepted

## Context
The first backend example needed a REST API with routing, JSON, PostgreSQL, environment
configuration, authentication middleware, and tests. An earlier plan
sketched a `std/web` package (a `Ctx` type, a router wrapper, `db.tx`). Writing
the example first showed what Lyth actually needs: Go 1.22's `ServeMux` already
routes by method and path (`"GET /v1/todos/{id}"`, `r.PathValue`), `pgx` and
`log/slog` are the de facto choices, and `http.Handler` middleware composes
with plain functions.

## Decision
- Lyth ships no web framework for now. Services use `net/http`,
  `encoding/json`, `log/slog`, `pgx`, and `context` directly, with their Go
  names. `examples/todoapi` is the reference: about 680 lines of Lyth (plus 290
  lines of tests) for a complete authenticated JSON API with PostgreSQL,
  graceful shutdown, and unit and integration tests.
- The language closes the gaps that example exposed instead: explicit type
  arguments for generic Go functions (`errors.AsType<*apiError>(err)`,
  `pgx.RowToStructByName<Todo>`), lambdas converted to named function types
  (`http.HandlerFunc({ w, r -> ... })`), type tests (`if (store is pinger)`),
  type aliases for handler types, nullable values passed to Go APIs (ADR 0017),
  and `catch` statements whose handler produces no value.
- Helpers that every service writes (JSON responses, error mapping) stay in
  application code until several programs show the same shape.

## Alternatives
- **`std/web` now**: it would fix an API before there is evidence for it,
  duplicate `ServeMux`, and give Lyth users a different vocabulary from Go
  documentation and tutorials. Overdesign at this stage.
- **Wrapping Go libraries in Lyth-flavored APIs**: against the core principle
  that interop needs no wrappers. The wrappers would also lag behind upstream.

## Consequences
Go knowledge transfers directly: the generated code is the Go a reviewer
expects (`http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {...})`,
`errors.AsType[*apiError](err)`). Missing conveniences show up as language or
interop gaps, which is where they should be fixed. A future library can still
add a thin layer once real programs justify it.
