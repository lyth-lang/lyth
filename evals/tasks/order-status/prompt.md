Write an enum `Status` with the values Pending, Paid, Shipped, Delivered and Cancelled.
Write `next(s)`, which returns the status that follows `s` (Pending, Paid, Shipped, Delivered),
or null for Delivered and Cancelled; `canCancel(s)`, true for Pending and Paid; and `label(s)`,
a description of each status. Use `when` over the enum without an `else` branch, so the
compiler checks that every status is covered.

Starting from Pending, print each status with its label and whether it can be cancelled,
following `next` until there is none. Then print Cancelled with its label and whether its
next status is null.

The program must print exactly:

```
Pending: waiting for payment, can cancel: true
Paid: paid, packing, can cancel: true
Shipped: on the way, can cancel: false
Delivered: delivered, can cancel: false
Cancelled: cancelled, next: true
```
