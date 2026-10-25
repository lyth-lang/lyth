# ADR 0036: Automatic `json` tags: `omitzero` for optional fields, initialisms, tags without a name

**Status:** Accepted (2026-10-25). Amends [0013](0013-struct-fields-and-json.md)
and the key rule of [0022](0022-capitalization-is-visibility.md).
Breaking: some JSON keys change, and null optional fields stop appearing.

## Context

Lyth gives every exported field a `json` tag with a lower-camel key unless its
tag already mentions `json` (ADR 0013). ADR 0013 notes that the rule freezes
once people depend on it. Three parts of it misfire today.

- **Initialisms are mangled.** `JSONName` lowers the leading uppercase run and
  keeps its last letter for the next word (`HTTPServer` → `httpServer`). Before
  a short lowercase suffix that splits the initialism itself:

  | Field | Key today | Expected |
  |---|---|---|
  | `IDs` | `iDs` | `ids` |
  | `URLs` | `urLs` | `urls` |
  | `IPv4` | `iPv4` | `ipv4` |
  | `APIs` | `apIs` | `apis` |

  Nothing reports it: the API contract is simply wrong. The same names flow into
  validator field names and into the JSON schemas that MCP and huma generate.
- **Optional fields look required.** A `T?` field means "may be absent", but its
  tag is `json:"nick"`. Schema generators read a missing `omitempty`/`omitzero`
  as "required": huma rejected a request body without `"nick"` (`expected
  required property nick to be present`). The reference API already writes
  `,omitempty` by hand on every optional field it returns (`Due`, `Next`).
- **A tag without a name switches the key.** Writing `json:",omitempty"` to add
  an option counts as "the tag mentions json". The key falls back to Go's
  default, the field name (`"NextCursor"`), next to lower-camel keys
  (`"items"`) in the same object.

## Decision

1. **Optional fields get `omitzero`.** An exported field whose type is
   nullable (`T?`, and nullable pointers, interfaces, functions, and channels)
   gets `json:"name,omitzero"`.
   - `omitzero` (Go 1.24) leaves the field out exactly when it is null, in
     `encoding/json` v1 and v2 alike.
   - `omitempty` would differ between them: v2 also drops a present `""`.
   - huma and `github.com/google/jsonschema-go` (the MCP SDK) treat `omitzero`
     as optional; checked in their sources.
   - Non-null fields, slices, and maps get no option: an empty list is data.
2. **Initialisms follow a fixed rule built on Go's canonical list.** The key
   is the field name with its first word lowered; the rest is kept as
   written (`UserID` → `userID`). Let *R* be the name's leading run of
   uppercase letters. The first word is:
   - *R* itself, when *R* ends the name or a digit follows it (`ID`, `URL`,
     `UTF8Name` → `utf8Name`);
   - *R* and a following `s`, when that `s` ends the name or an uppercase
     letter or digit follows it: the plural of an initialism (`IDs`, `URLs`,
     `SKUs`, `UIDsByHost`);
   - otherwise, when a lowercase letter follows a run of two or more
     letters, *R* without its last letter, which starts the next word
     (`HTTPServer` → `HTTP` + `Server`, `APIKey` → `API` + `Key`), unless
     *R* is in the canonical list and *R* without its last letter is not: then
     the first word is *R* (`IPv4` → `ipv4`);
   - otherwise the leading capital (`Name`, `User`).

   The canonical list is Go's own, golint's `commonInitialisms` (the list Go
   style checkers enforce): ACL, API, ASCII, CPU, CSS, DNS, EOF, GUID, HTML,
   HTTP, HTTPS, ID, IP, JSON, LHS, QPS, RAM, RHS, RPC, SLA, SMTP, SQL, SSH, TCP,
   TLS, TTL, UDP, UI, UID, UUID, URI, URL, UTF8, VM, XML, XMPP, XSRF, XSS. It
   only decides runs like `IP` in `IPv4`; every other case follows the rule.

   | Field | Key | Why |
   |---|---|---|
   | `Name`, `ID`, `URL` | `name`, `id`, `url` | one word |
   | `UserID`, `UserIDs` | `userID`, `userIDs` | only the first word is lowered |
   | `IDs`, `URLs`, `SKUs` | `ids`, `urls`, `skus` | plural of an initialism |
   | `HTTPServer`, `HTTPSPort`, `APIKey`, `TTLSeconds` | `httpServer`, `httpsPort`, `apiKey`, `ttlSeconds` | the run's last letter starts a word |
   | `IPv4`, `UTF8Name` | `ipv4`, `utf8Name` | a listed initialism keeps its letters |
   | `ETag`, `OAuthToken`, `XCoord` | `eTag`, `oAuthToken`, `xCoord` | exceptions: see below |

   **Exceptions need an explicit tag.** When the rule splits a run of
   uppercase letters after its first letter (`ETag` → `e` + `Tag`), the name
   may be one word written in mixed case (`ETag`, `OAuth`) or two (`XCoord`).
   The compiler cannot know which, so it warns (W0008) and the field needs a
   tag: `json:"etag"`, `json:"oauthToken"`, or `json:"xCoord"` to keep the
   automatic key. Every other name gets its key without a tag or warning.
   The same function names enum values on the wire
   ([ADR 0035](0035-enums-on-the-wire.md)).
3. **A tag without a name gets the automatic name.** `json:",omitempty"`
   becomes `json:"nextCursor,omitempty"`, and `json:",string"` becomes
   `json:"count,string"`. A tag with a name is used verbatim. `json:"-"` still
   hides the field, and `json:"-,"` still means the key `-`. An explicit
   `omitempty` or `omitzero` on a `T?` field replaces the automatic option.
4. **Embedded fields get no automatic tag**, so `encoding/json` flattens them
   as in Go ([ADR 0043](0043-struct-embedding.md)).

The docs recommend `encoding/json/v2` (standard in Go 1.27) for new code: it
encodes nil slices as `[]`, matching Lyth's "slices are never null", where v1
writes `"items":null`.

## Alternatives

- **`omitempty` for `T?`.** It is the familiar spelling, but under v2 it also
  drops `""`, `[]`, and `{}`, so `Email: string?` set to `""` would vanish.
- **No option for `T?`; explicit `null` stays.** It keeps today's wire format,
  but schema generators keep declaring every optional field required, and
  every API writes the tag by hand.
- **A longer list of known words** (golint's initialisms plus `ETag`,
  `OAuth`, `IPv6`, …). It handles more names silently, but the list is never
  complete and is frozen with the wire format; a name it misses changes keys
  without a word. The canonical list plus a warning for the ambiguous shape
  keeps the rule small and makes every exception visible.
- **The previous rule unchanged** (`IDs` → `iDs`, `URLs` → `urLs`, `IPv4` →
  `iPv4`). Predictable, but wrong for the most common plural field names.
- **snake_case keys, or a per-package choice.** Many APIs are snake_case, and
  the review's data-layer API tagged every field. Adding an opt-in later is not
  breaking, so it is left out here.
- **Warn instead of changing anything.** It is honest about the rule but leaves
  every new project to discover it, and the rule cannot change after 1.0.

## Consequences

- **Null optionals.** They disappear from JSON output instead of appearing as
  `null`. Clients that test `x === null` instead of a missing key need the tag
  `json:"x"`.
- **Renamed keys.** Fields named like `IDs`, `URLs`, or `IPv4` get new keys.
- **Schemas.** Schemas generated from Lyth structs mark optional fields as
  optional.

### Migration
This is a wire change, not a source change, so `lyth fix` has nothing to
rewrite.
- **Warning for one release.** For one release, `lyth check` warns (W0009) at each
  field whose json key changed ("the json key of IDs is now ids; it was iDs
  before Lyth 0.4.0"), with a hint giving the explicit tag that keeps the old
  key. That includes fields whose tag has no name (`json:",omitempty"`):
  before this ADR such a tag was used as written, so Go's key was the field
  name itself (`NextCursor`, `IDs`). The warning names the release after
  0.3.0; it is removed in a later release.
- **Duplicate keys.** Two fields of a struct with the same json key (`IDs` and
  `Ids` are both `ids`) warn (W0008): `encoding/json` leaves both out, which
  only `go vet` reported before.
- **Decoding old data.** `encoding/json` (v1) matches keys without regard to
  case when it decodes, so stored JSON with the old keys (`iDs`, `Count`)
  still decodes into the renamed fields; `encoding/json/v2` matches exactly
  and does not.
- **Version table.** The guide's version table gains a row.

### Implementation
- `JSONName` and `fieldTag` in `internal/check/decl.go`, with the canonical
  list, warning W0008 for the exceptions, and the migration warning W0009.
  The table above is the unit test `internal/check/jsonname_test.go`.
- `run/jsonkeys` (keys, name-less tags, `omitzero`), `run/jsonv2` (optional
  fields round-tripped through v1 and v2), and `check/jsonkeys_warn` (W0008,
  W0009).
- The spec (language.md §3.3) states the rule.
