Write a generic `Stack[T]` with `Push(v)`, `Pop()` and `Len()`. `Pop` returns the top value
and whether there was one (a `(T, bool)` pair), and removes it. Write a generic function
`reversed(xs)` that reverses a slice using a stack.

In `main`, push the squares of 1 to 4 onto a stack of ints and print its size, pop and print
the top, and print the size again. Pop from an empty stack of strings and print whether it
worked. Finally print `reversed` of `["a", "b", "c"]` and of `[1.5, 2.5]`.

The program must print exactly:

```
size: 4
popped: 16
size: 3
empty pop ok: false
[c b a]
[2.5 1.5]
```
