# ADR 0043: Struct embedding

**Status:** Accepted (2026-10-25). Implements the **[planned]** item of the
spec (language.md §3.3) and removes it from the documented limitations
(overview §5). Not breaking: it adds syntax that is a parse error today. It is
listed with the decisions because its spelling must be fixed before people
work around it.

## Context

Lyth structs cannot embed. Promotion through embedded fields of *Go* structs
already works, because member lookup on Go types is delegated to `go/types`
(`goMember` in `internal/check/member.go`). But a Lyth struct cannot embed
anything, and the review found that this blocks or bloats common backend
code.

- **gRPC.** Generated server interfaces contain the unexported method
  `mustEmbedUnimplemented…Server()`, which only embedding can provide. A Lyth
  server does not compile against default `protoc-gen-go-grpc` output. The
  workarounds are the generator flag `require_unimplemented_servers=false`,
  which gives up forward compatibility, or a hand-written Go adapter inside
  the generated package.
- **JWT.** golang-jwt custom claims embed `jwt.RegisteredClaims`. Without
  embedding, six forwarding methods are needed, and `exp`/`iat` nest under a
  field instead of sitting at the top level of the token. Other services then
  cannot read them.
- **Middleware.** A status-recording `http.ResponseWriter` forwards every
  method by hand.
- **Shared fields.** Embedded mutexes (`sync.Mutex`), testify's `mock.Mock`
  and `suite.Suite`, and shared fields flattened into JSON (`Timestamps` in
  every model) all need embedding.
- **Partial test fakes.** Go embeds the interface in the fake and overrides
  one method. In the review, a fake implemented all 10 methods of sqlc's
  `Querier`, about 110 lines.
- **Diagnostics.** Go's embedding syntax gives raw parse errors today
  (`expected ':' after field name`). In one gRPC server it cascaded to 14
  errors, although the overview promises a specific diagnostic for each
  limitation.

## Decision

- **Syntax.** Go's spelling: a type alone on a line of the struct body embeds
  it.

  ```kotlin
  struct statusRecorder {
      http.ResponseWriter
      status: int
  }

  func *statusRecorder.WriteHeader(code: int) {
      self.status = code
      self.ResponseWriter.WriteHeader(code)
  }

  struct Claims {
      jwt.RegisteredClaims
      UserID: string
  }

  struct server {
      pb.UnimplementedGreeterServer
      store: Store
  }
  ```

  The embedded type is a type name, optionally qualified, instantiated, or
  behind `*`: `T`, `*T`, `pkg.T`, `Page[int]`. Embedding a type parameter is an
  error, as in Go.
- **The field.** The field's name is the type's name without package or `*`
  (`ResponseWriter`, `RegisteredClaims`). It is exported when that name is
  exported (Go's rule).
- **Construction.** Embedded fields are constructed by that name
  (`statusRecorder(ResponseWriter = w)`) and take part in positional
  construction in declaration order.
- **Promotion.** It follows Go exactly: promoted fields, promoted methods,
  method sets, depth, and ambiguity. Lyth delegates it to `go/types` over the
  shadow package, as it does for Go structs. Promoted Lyth members keep their
  Lyth types (nullability, `throws`). A method declared on the outer type hides
  a promoted one of the same name, so the example's `WriteHeader` overrides
  the writer's. Interface satisfaction counts promoted methods, which is what
  makes the gRPC and jwt cases work.
- **Access.** Promoted members are reached through a value, or through `self`
  inside methods (`self.Header()`, `self.Lock()`;
  [ADR 0038](0038-members-through-self.md)).
- **Nullability.**
  - **Non-null embedded types must be given at construction.** An embedded
    pointer, interface, function, or channel type is non-null, so it has no
    zero value. Embedded value types are zeroable as usual (`sync.Mutex`,
    `RegisteredClaims`).
  - **A nullable embedded type is allowed (`todo.Store?`).** It exists for
    partial fakes. Its promoted members still make the outer type satisfy
    interfaces (Go's method set), so a call through the interface reaches a
    nil embedded value and panics, which is the Go behaviour fakes rely on.
    A direct access in Lyth code (`fake.Delete()`) needs the field checked
    first: E0401, "Delete is promoted through the nullable field Store".
- **JSON.** Embedded fields get no automatic `json` tag, so `encoding/json`
  flattens embedded structs as in Go. An explicit tag on the embedding line
  nests it: `Timestamps` followed by the tag `json:"ts"` puts its fields under
  `"ts"`.
- **Sealed interfaces.** Embedding a member of a sealed interface does not make
  the outer struct a member. Membership is declared (ADR 0025). Converting the
  outer struct to the sealed interface is E0201, which says so.

## Alternatives

- **A keyword (`embed http.ResponseWriter`, `...Base`).** It is more visible,
  but it is not Go's spelling, and Go readers already know a lone type line.
- **Delegation with explicit forwarding (Kotlin's `by`).** It gives no field
  and no Go method-set promotion, so the gRPC and jwt cases stay impossible,
  and it lowers to many generated methods.
- **Embedding only Go types.** It covers gRPC, jwt, and mutexes, but not shared
  Lyth models or partial fakes, and it leaves an odd rule to teach.

## Consequences

- **What works.** gRPC services work with default code generation; jwt claims,
  `ResponseWriter` wrappers, embedded mutexes, testify suites and mocks,
  partial fakes, and flattened JSON read as in Go.
- **Nullable embedding.** It is the one place where a promoted call can panic
  on nil without `!!` (through an interface), and only on a field the author
  declared `T?`. The spec says so.
- **The limitation goes.** The overview's limitation list and the guide's "Not
  supported yet" lose struct embedding.

### Migration
None: embedding lines are parse errors today.

### Implementation
- **Parser.** A `FieldDecl` alternative for a lone type line. The E0003
  recovery for `pkg.Type` lines becomes the real parse.
- **Types and checker.** `types.Field` gains `Embedded`. The shadow
  `go/types` struct embeds the field, so `LookupFieldOrMethod` resolves
  promotion. The checker maps a promoted Lyth member back to its Lyth
  declaration for nullability and `throws`, adds the nullable-embedding rule,
  the sealed-membership check, and the tag rule.
- **Lowering.** The field is emitted without a name.
- **Tests.**
  - `run/embedding`: `ResponseWriter` wrapper, mutex, shared fields, override,
    JSON flattening and a tagged embedding, a generic embedded type, and a
    partial fake whose null embedded interface panics when called through
    the interface.
  - `check/embedding_neg`: type parameter, pointer to an interface, a non-name
    type, ambiguous selector, nullable promotion, a promoted pointer method
    on a value receiver, sealed membership, construction, and Go's
    `Name Type` field spelling (E0008 with a fix).
  - `TestEmbeddingAcrossPackages`: a Go package shaped like
    `protoc-gen-go-grpc` output (an interface with the unexported
    `mustEmbedUnimplementedGreeterServer`), and a Lyth struct of another
    package whose promoted members keep their Lyth types. A generated gRPC
    service needs `protoc`, which the test suite does not run.
