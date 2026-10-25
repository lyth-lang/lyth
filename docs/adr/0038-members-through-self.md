# ADR 0038: Members are reached through `self.`

**Status:** Accepted (2026-10-25). Amends [0031](0031-go-spelling.md)
("fields and methods also remain in scope by name inside a method").
Breaking: a bare field or method name inside a method no longer means the
member.

## Context

Inside a method, fields and methods of the receiver are in scope by bare name.
`Count += n` means `self.Count += n`, and `authed(list)` means
`self.authed(self.list)`. The checker looks receiver members up before
package-level names, imports, and types (`lookup` in `internal/check/check.go`).
The review found four kinds of problem with that rule.

1. **Existing code silently changes meaning.** A member wins over every name
   outside the method:

   ```kotlin
   var limit = 50
   struct Query { limit: int }

   func Query.pageSize(): int {
       if self.limit > 0 { return self.limit }
       return limit        // the field (0), not the package variable (50)
   }
   ```

   - With a field named `log` and an import of `log`, `log.Println(…)` calls
     the field.
   - A method `validate` hides a package function `validate`.
   - So adding a field or method to a type can change existing method bodies
     with no diagnostic.
2. **Types and imports become unreachable.** With a field `Config: Config`, the
   type is unreachable inside the type's methods: `next: Config = …` is "Config
   is not a type". A method `Stats(): Stats` hides its own result type. A field
   named `http` hides `net/http`. These are everyday Go layouts, and none of
   the errors mention `self`.
3. **A safety check is skipped.** In a value-receiver method,
   `self.hits.Add(1)` is correctly E0502 (a pointer method on a copy). The bare
   `hits.Add(1)` compiled and incremented a copy (`hits: 0`). The mutex version
   raced and deadlocked under `lyth test -race`.
4. **Warnings and bugs exist only because of the rule.**
   - W0006 fires when a local reuses a field name (`cfg := cfg.Load()`).
   - A lambda-scope bug was fixed in the release polish: a lambda in a method
     read a field instead of an enclosing local of the same name.
   - In concurrent code, `mu.Lock(); seq++; jobs.send(job)` hides which names
     are shared state.

Go has no implicit receiver: every member access is `s.x`. Go reviewers rely on
that, and the pitch of Lyth is code that is easy to review.

## Decision

- **Members need `self.`** Inside a method, fields and methods of the receiver
  are reached only through `self`: `self.Count`, `self.authed(self.list)`. A
  bare name resolves exactly as it would outside the method: locals and
  parameters, then package-level declarations and imports, then predeclared
  names.
- **No exception for methods.** The rule covers calls to other methods
  (`self.validate()`) and method values (`self.list`) as well as fields. A
  single rule is easier to read and to teach than "fields need `self`,
  methods don't".
- **W0006 no longer has a field case.** A local can no longer hide a field.
  It still warns when a local hides an outer local or parameter.
- **The value-receiver checks always apply.** Every member access now goes
  through `self`, so E0502 and E0504 apply to all of them. That closes the
  copy-mutation hole in point 3 with no separate fix.
- **Generated Go is unchanged.** The receiver is still named after the type
  (`s.Count`).

## Alternatives

- **Keep bare names (status quo).** Shortest, and familiar from Kotlin, Swift,
  and Java. It keeps all four problems: the first two can be patched with
  errors, the last two cannot.
- **Keep bare names, but make collisions an error.** When a bare name
  could also mean a local, import, package-level declaration, or type, report
  an error. This removes the silent cases (points 1 and 2) without changing
  most code, but readers still cannot tell `authed(list)` from a function
  call or `seq++` from a package counter. The value-receiver check would
  still need fixing separately. This is the fallback if this ADR is rejected.
- **Go's explicit receiver names** (`func (s *Server) M()`). ADR 0031 rejected
  them: a different name in every method, which locals can shadow. `self` is
  the same idea with one name.
- **A shorter sigil (`.count`, `@count`).** Shorter than `self.`, but it is new
  syntax with no Go counterpart, and `.x` already means something else in Swift
  (an implicit enum member).

## Consequences

- **Cost.** Member accesses cost five characters; the line count does not
  change. `examples/todoapi` has about 60 implicit member accesses in 29
  methods. Expression bodies on small value types read longer:
  `func Point.Len(): float64 = math.Sqrt(self.X * self.X + self.Y * self.Y)`.
  Go writes `p.X*p.X`.
- **Readability.** Readers see shared state and method calls at a glance, as
  in Go.
- **Names no longer collide.** A field may share its name with a type, an
  import, or a package-level declaration.
- **Interaction with embedding.** Promoted members come through `self` as well
  (`self.Header()`), so embedding does not widen the set of bare names in
  scope ([ADR 0043](0043-struct-embedding.md)).

### Migration
The old resolution is still computed, so the change can be made without any
silent difference:
- **Bare member names are errors for at least the following releases.** A bare
  name that the old rule resolved to a member is an error ("count is a field
  of self; write self.count"), whose edit inserts `self.`. This holds even
  when the name now also resolves to something else, so the package-level
  `limit` never takes over silently.
- **The edit is a fix only where it is unambiguous.** Where the name can mean
  only the member, the edit respells what the code meant, and `lyth fix`
  migrates a module in one pass. Where the name also names something valid
  in that position (a package-level variable, constant, or function; an
  import left of a dot; a type or builtin that is called), code written
  before this ADR meant the member but code written since may mean the other
  name. The error names both ("limit is a field of self and also names the
  package-level variable limit"), the edit is a hint, and `lyth fix` leaves
  the choice to the author.
- **Calls of fields that are not functions cannot have meant the member,**
  so they take the new meaning without an error during the migration window,
  because calling such a field never compiled: `Config(Name = "x")` with a
  field `Config: Config` constructs the type, `max(a, b)` with a field
  `max: int` calls the builtin, and `strings.Join(...)` with a field
  `strings: []string` (which has no `Join`) calls the package.
- **After that,** bare names resolve normally (locals, then package-level),
  as in any explicit-receiver language.
- **Docs.** A `testdata/fix` case and a row in the guide's version table. The
  guide, tour, cheat sheet, spec (§3.2), examples, std, bench, and evals
  reference solutions are migrated by `lyth fix` in the same change.

### Implementation
- **Lookup.** `lookup` in `internal/check/check.go` stops consulting
  `receiverMember` and uses it only to produce the migration error.
- **W0006.** Its field case goes from `internal/check/stmt.go`.
- **Tests.**
  - `check/members_neg`: bare fields and methods, collisions with a package
    variable, a function, an import, a type, and a builtin, lambdas, method
    values, and locals of a member's name.
  - `fix/self`: `lyth fix` inserts `self.` where only the member is meant,
    and leaves calls of a type or builtin named like a field.
  - `TestFixLeavesAmbiguousMembers`: `lyth fix` leaves ambiguous names, and a
    second run changes nothing.
  - `run/selfnames`: fields named like a type, an import, and a local.
  - Every Lyth source in the repository was migrated with the fix.
