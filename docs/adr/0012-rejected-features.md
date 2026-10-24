# ADR 0012: Features considered and rejected

**Status:** Accepted; extended by [0031](0031-go-spelling.md), which adopts `:=` (rejected here) as the declaration of locals

| Feature | Why rejected | Instead |
|---|---|---|
| `:=` declarations | Second syntax for `val`, read as mutable by Go users | `val`/`var`; E0005 explains |
| Fallibility inferred from `?` in the body | API changes with implementation, invisible in signatures | explicit `throws` |
| Implicit numeric widening (`Float64 * Int`) | Hides precision loss and overflow, one of Go's real strengths | E0202 with a fix-it |
| `parallel for ... limit N` syntax | Library-expressible with fallible lambdas, and would freeze semantics in the language | `lyth/conc` (backend-concurrency.md §3) |
| lowerCamel aliases for Go APIs | Heuristics, collisions, two spellings | verbatim Go names (ADR 0010) |
| Receiver lambdas / implicit DSL receivers (`get(...) { json(x) }`) | Hidden name resolution, type-system complexity | explicit lambda parameter, `it` |
| Dynamic route params `params.id` | Needs framework knowledge in the compiler or dynamic typing | `c.PathValue("id")` |
| Exceptions with unwinding | Hidden control flow, Go incompatibility | `throws`/`?`/`catch` |
| Operator overloading, macros | Unpredictable generated code; compile-time code execution | functions |
| `$name` template shorthand | Two template syntaxes; `${name}` suffices | `${...}`; E0006 catches `$name` |
| Triple-quoted strings | Overlaps with Go raw strings | backtick raw strings |
| Function overloading | Go has none; mangled names hurt interop | distinct names |
