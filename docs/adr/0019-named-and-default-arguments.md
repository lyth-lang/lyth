# ADR 0019: Named and default arguments for Lyth functions

**Status:** Accepted

## Context
Backend functions accumulate optional settings: timeouts, page sizes, retry
counts, flags. Go answers with option structs, functional options, or several
functions per variant, and boolean arguments stay unreadable at call sites
(`render(t, false, true)`). Kotlin answers with default values and named
arguments. Lyth constructs structs with named arguments already, so calls
looked inconsistent without them.

## Decision
- A parameter may declare a default value. Calls to functions and methods
  declared in Lyth may name arguments: positional ones first, named ones in any
  order. A trailing lambda fills the last parameter; a variadic parameter is
  positional only.
- Defaults are filled in at the call site. The Go signature stays exactly what
  the parameter list says, and a call lowers to an ordinary positional call.
- A default is resolved where the function is declared, so a caller's locals
  cannot change it. It must be a plain expression (no control flow and no error
  or null operators) and cannot refer to other parameters. In a `pub` function
  it may use only `pub` names, because calls in other packages contain it.
- Arguments are evaluated in the order written, then the defaults. Explicit
  arguments move into temporaries only when the positional Go call would
  otherwise run side effects in a different order.
- Go functions take positional arguments only.

## Alternatives
- **Defaults implemented in the callee** (pointer or variadic parameters, or a
  generated wrapper per arity): changes the API that Go callers see, or adds
  functions. Call-site expansion keeps Lyth and Go signatures identical.
- **Defaults that refer to earlier parameters** (`end: Int = start`): useful
  occasionally, but it needs temporaries for every such call. It can be added
  later without breaking code.
- **Named arguments for Go functions**: Go parameter names are not part of an
  API. They change between versions, and can be unnamed, without Go considering
  it a breaking change. Lyth code would break when a dependency renamed one.
- **Option structs only**: still the right tool for large configurations, and
  struct construction already takes named fields (`http.Server(Addr = ...)`).

## Consequences
Go callers of a Lyth package pass every argument. Since Go builds every package
from source, a changed default reaches all callers at their next build.
Interface methods cannot declare defaults, because a call through an interface
does not know which implementation's defaults would apply. Adding a parameter
with a default is compatible for Lyth callers but not for Go callers.
