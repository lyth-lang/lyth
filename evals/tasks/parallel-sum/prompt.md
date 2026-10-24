Sum the numbers from 1 to 1,000,000 with 4 goroutines, each summing one consecutive quarter
of the range and sending its result over a channel. Print each worker's sum in worker order,
then the total.

The program must print exactly:

```
worker 0: 31250125000
worker 1: 93750125000
worker 2: 156250125000
worker 3: 218750125000
total: 500000500000
```
