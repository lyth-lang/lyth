Translate this Go program into Lyth, using Lyth's error handling (`throws`, `throw`, `?`,
`catch`) instead of `if err != nil`:

```go
package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Entry struct {
	Key   string
	Value int
}

var ErrEmpty = errors.New("empty line")

func parseLine(raw string) (Entry, error) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return Entry{}, ErrEmpty
	}
	key, text, ok := strings.Cut(line, "=")
	if !ok {
		return Entry{}, fmt.Errorf("line %q: missing '='", line)
	}
	name := strings.TrimSpace(key)
	v, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return Entry{}, fmt.Errorf("key %s: %w", name, err)
	}
	return Entry{Key: name, Value: v}, nil
}

func parseAll(text string) (map[string]int, []error) {
	out := map[string]int{}
	var errs []error
	for _, line := range strings.Split(text, "\n") {
		e, err := parseLine(line)
		if err != nil {
			if !errors.Is(err, ErrEmpty) {
				errs = append(errs, err)
			}
			continue
		}
		out[e.Key] = e.Value
	}
	return out, errs
}

func main() {
	text := "workers = 4\n\nretries=3\ntimeout = 3s\nverbose\nport= 8080\n"
	values, errs := parseAll(text)
	fmt.Println(values)
	for _, err := range errs {
		fmt.Println("error:", err)
	}
}
```

The program must print exactly:

```
map[port:8080 retries:3 workers:4]
error: key timeout: strconv.Atoi: parsing "3s": invalid syntax
error: line "verbose": missing '='
```
