# todoapi: a REST API in Lyth

A complete JSON API for per-user todo lists, written in Lyth on ordinary Go
libraries: `net/http` (Go 1.22 routing), `encoding/json`, `log/slog`, and
PostgreSQL through `pgx`. It is the reference program for Lyth's backend story
([ADR 0015](../../docs/adr/0015-backend-on-go-libraries.md)).

| Concern | Where | How |
|---|---|---|
| Configuration | `internal/config` | environment variables, validated at start (`config.Load(os.Getenv)`) |
| Routing | `internal/api/api.lyth` | `http.ServeMux` patterns such as `"PATCH /v1/todos/{id}"` |
| Authentication | `internal/api/api.lyth` | bearer tokens compared in constant time (`crypto/subtle`) |
| JSON | `internal/api` | struct construction, automatic `json` tags, 1 MiB body limit, unknown fields rejected |
| Errors | `internal/api/errors.lyth` | `throws`/`?` in handlers; one place maps errors to 400/401/404/413/422/500 |
| Validation | `internal/todo/todo.lyth` | `NewTodo.Normalize()` throws a `*ValidationError` with per-field problems (422) |
| Storage | `internal/todo` | a `Store` interface; PostgreSQL (`pgstore.lyth`) and in-memory (`memstore.lyth`) |
| Middleware | `internal/api/middleware.lyth` | request logging and panic recovery as `http.Handler` wrappers |
| Shutdown | `main.lyth` | `signal.NotifyContext`; a goroutine waits on `ctx.Done().receive()`, calls `srv.Shutdown`, and closes a `done` channel |
| Tests | `*_test.lyth`, `memstore_go_test.go` | `httptest` against the in-memory store; PostgreSQL integration test; a plain Go test calling the Lyth code |

## API

All `/v1` endpoints need `Authorization: Bearer <token>`. Each user sees only
their own todos.

| Method and path | Result |
|---|---|
| `GET /healthz` | `{"status":"ok"}`, or 503 when the database is unreachable |
| `GET /v1/todos?done=true&limit=50&after=ID` | one page: `{"todos": [...], "next": ID}` |
| `POST /v1/todos` `{"title": "...", "due": "2026-11-01T09:00:00Z"}` | 201 with `Location` |
| `GET /v1/todos/{id}` | the todo, or 404 |
| `PATCH /v1/todos/{id}` `{"title"?, "done"?, "due"?}` | the changed todo |
| `DELETE /v1/todos/{id}` | 204 |

Errors share one shape: `{"error": {"code": "invalid", "message": "invalid
input", "fields": {"title": "must not be empty"}}}`.

## Run it

From this directory, with `lyth` installed (`go install ./cmd/lyth` at the
repository root) and a PostgreSQL database:

```bash
createdb todo
```

```bash
DATABASE_URL=postgres:///todo API_TOKENS=alice:alice-secret,bob:bob-secret lyth run .
```

```bash
curl -s -X POST -H "Authorization: Bearer alice-secret" -d '{"title":"ship it"}' localhost:8080/v1/todos
```

The schema is created on start. `ADDR`, `LOG_LEVEL`, and `SHUTDOWN_TIMEOUT`
are optional (see `internal/config/config.lyth`).

## Test it

```bash
lyth test ./...
```

```bash
DATABASE_URL=postgres:///todo_test lyth test -v -run TestPgStore ./internal/todo
```

The first command runs the unit tests (HTTP handlers through `httptest`
against the in-memory store, validation, configuration) and the Go-written
test. The PostgreSQL integration test is skipped unless `DATABASE_URL` is set;
it uses a unique owner per run and deletes its rows afterwards.

To read the Go that Lyth generates:

```bash
lyth gen -stdout ./internal/api
```
