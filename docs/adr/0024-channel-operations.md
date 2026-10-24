# ADR 0024: Channel operations are methods; `select` has `when`-style branches

**Status:** Accepted; channel types respelled `chan T`, `chan<- T`, `<-chan T`, and select bindings `v := ch.receive()`, by [0031](0031-go-spelling.md)

## Context
Channels are planned (roadmap phase 4b). Go spells their operations with the
`<-` operator: `ch <- v` sends, `<-ch` receives, and `close(ch)` closes. The
operator is the least readable part of Go's syntax for programmers new to Go,
and it is a lexing hazard in Lyth, where `x<-1` would mean a send rather than
the comparison `x < -1`. Kotlin's coroutine channels use `send` and
`receive`, which Kotlin programmers already know, and Go channel types have no
methods that these names could collide with.

## Decision
- Channel types are `Chan<T>`, `SendChan<T>` (Go `chan<- T`), and
  `ReceiveChan<T>` (Go `<-chan T`; previously spelled `RecvChan<T>`). Channels
  are created with `make(Chan<T>)` or `make(Chan<T>, n)`, as slices and maps
  are.
- Operations are methods that lower to Go's operators with no added cost:

  | Lyth | Go |
  |---|---|
  | `ch.send(v)` | `ch <- v` |
  | `ch.receive()` | `<-ch` |
  | `val (v, ok) = ch.receive()` | `v, ok := <-ch` |
  | `ch.close()` | `close(ch)` |

  `send` is a statement; it has no value. `send` and `close` need a
  `Chan<T>` or `SendChan<T>`, `receive` a `Chan<T>` or `ReceiveChan<T>`.
  `for (v in ch)`, `len(ch)`, and `cap(ch)` are unchanged.
- `receive()` returns `T?` when `T` has no zero value (a non-null pointer,
  interface, function, or channel), since a closed channel yields nil. This
  is the rule map lookups already follow, and it gives the idiom
  `val job = jobs.receive() ?: break`.
- `go` and `defer` take a function call; a send or receive is not one, so
  `go ch.send(v)` is E0216 with the hint `go { ch.send(v) }`. Go has the same
  rule; wrapping in a closure would silently move the evaluation of `v` into
  the goroutine.
- `select` waits on several operations. Each branch is one channel operation
  followed by `->` and a body, as in `when`; `else` is Go's `default`:

  ```
  select {
      val job = jobs.receive() -> handle(job)
      val (r, ok) = results.receive() -> if (!ok) return
      out.send(next) -> sent++
      ctx.Done().receive() -> return
      else -> idle()
  }
  ```

  As in `when`, `break` inside a `select` branch exits the enclosing loop, not
  the `select`. `select` is a statement, not an expression: branches usually
  return, send, or loop, and a value form would need an `else` that a
  blocking `select` does not have.
- Semantics are Go's: an unbuffered send waits for a receiver, receiving from
  a closed channel returns the zero value (`ok` is false), sending on a closed
  channel panics, and a nil channel blocks forever. Kotlin's channels throw on
  closed channels instead; the guide documents the difference.

## Alternatives
- **Go's operators**: zero translation from Go code, but the syntax new users
  find hardest to read, and a lexing trap next to comparisons.
- **Constructor syntax `Chan<T>(n)`**: reads naturally, but adds a second way
  to make a channel next to `make`, which slices and maps also use.
- **Kotlin's `select { ch.onReceive { v -> } }`**: needs receiver lambdas and
  builder semantics that Lyth rejected (ADR 0012).

## Consequences
Translating Go examples means rewriting `<-` (`<-ctx.Done()` becomes
`ctx.Done().receive()`). The names cannot collide with a Go type's methods:
Lyth only calls exported Go methods, and these are lower-case. The checker
treats the three operations as built-in members of channel types (named Go
channel types included), like the prelude methods on strings. `select`
became a keyword.
