Write a program that counts the words in this text, ignoring case and punctuation (a word is
a run of letters):

```
The quick brown fox jumps over the lazy dog. The dog sleeps; the fox runs! A quick fox, a lazy dog.
```

Print the five most frequent words with their counts, most frequent first and alphabetically
among equal counts, then the number of distinct words. Keep the counts in a `map[string]int`.

The program must print exactly:

```
the: 4
dog: 3
fox: 3
a: 2
lazy: 2
distinct words: 11
```
