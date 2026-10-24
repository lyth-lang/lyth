Write a sealed interface `Shape` with an `Area(): float64` method, implemented by `Circle`
(radius), `Rect` (width and height) and `Triangle` (base and height). Write `describe(s)`
with a `when` over the shape's type that names it; a rectangle with equal sides is a square.

For a circle of radius 1.5, a 2 by 3 rectangle, a 4 by 4 rectangle and a triangle with base 6
and height 2, print each description with its area to two decimals, then the total area.

The program must print exactly:

```
circle of radius 1.5: 7.07
rectangle 2x3: 6.00
square of side 4: 16.00
triangle with base 6: 6.00
total area: 35.07
```
