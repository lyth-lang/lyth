// Package fixtures generates deterministic inputs shared by the Lyth and Go
// implementations of each benchmark workload.
package fixtures

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
)

// OrderJSON returns a JSON order with the given number of items, in the
// shape both jsoncodec implementations decode.
func OrderJSON(items int) []byte {
	var b strings.Builder
	b.WriteString(`{"id":4711,"customer":{"id":42,"name":"Ada Lovelace","email":"ada@example.com","vip":true},"items":[`)
	for i := 0; i < items; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"sku":"SKU-%05d","quantity":%d,"priceCents":%d}`, i, i%5+1, 199+i*7)
	}
	b.WriteString(`],"note":"leave at the door","tags":["express","gift","fragile"]}`)
	return []byte(b.String())
}

var endpoints = []string{"/users", "/orders", "/orders/{id}", "/health", "/login", "/search", "/cart", "/checkout"}

// LogLines returns n access-log lines "timestamp,user,endpoint,status,latency_ms".
func LogLines(n int) []string {
	r := rand.New(rand.NewPCG(1, 2))
	lines := make([]string, n)
	for i := range lines {
		status := 200
		switch x := r.IntN(100); {
		case x < 3:
			status = 500
		case x < 8:
			status = 404
		}
		lines[i] = strconv.Itoa(1700000000+i) + ",user" + strconv.Itoa(r.IntN(500)) + "," +
			endpoints[r.IntN(len(endpoints))] + "," + strconv.Itoa(status) + "," + strconv.Itoa(5+r.IntN(400))
	}
	return lines
}

var vocabulary = strings.Fields(`the quick brown fox jumps over lazy dog go lyth kotlin compiler type
error value pointer slice map channel interface struct method function package import module
build test bench lambda closure template string number boolean null safe call elvis`)

// Docs returns n documents as (id, text) pairs.
func Docs(n int) ([]int, []string) {
	r := rand.New(rand.NewPCG(3, 4))
	ids := make([]int, n)
	texts := make([]string, n)
	for i := range ids {
		ids[i] = i + 1
		words := make([]string, 8+r.IntN(24))
		for j := range words {
			w := vocabulary[r.IntN(len(vocabulary))]
			if r.IntN(10) == 0 {
				w = strings.ToUpper(w[:1]) + w[1:]
			}
			words[j] = w
		}
		texts[i] = strings.Join(words, " ")
	}
	return ids, texts
}

// Items returns n work item names.
func Items(n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = "item-" + strconv.Itoa(i)
	}
	return items
}
