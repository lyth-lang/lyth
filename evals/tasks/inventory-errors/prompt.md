Write an inventory program. The stock starts as apple 5, pear 2 and plum 0, kept in a
`map[string]int`.

Define two error types: `UnknownItem`, for an item the stock doesn't have, and `OutOfStock`,
for an order larger than the stock, with the messages shown below. Write a function
`take(stock, item, qty)` that removes `qty` units of `item` and returns how many are left, and
fails with one of those errors. Use Lyth's error handling (`throws`, `?`, `catch`), not
`if err != nil`.

Process these orders one at a time and print a line for each: apple 3, pear 5, kiwi 1,
apple 2, apple 1. Handle an unknown item with a `catch` for `UnknownItem` specifically, and any
other error with a general one. Then, in a function that propagates errors, take 1 pear and
then 1 plum, and print the error that stopped it. Finally print how many units are left in
stock altogether.

The program must print exactly:

```
apple x3: ok, 2 left
pear x5: error: not enough pear: wanted 5, have 2
kiwi x1: skipped, kiwi is not sold here
apple x2: ok, 0 left
apple x1: error: not enough apple: wanted 1, have 0
batch stopped: not enough plum: wanted 1, have 0
left: 1
```
