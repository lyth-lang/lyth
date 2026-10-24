// Package goimpl is the hand-written Go version of the index (allocation)
// workload.
package goimpl

import (
	"strconv"
	"strings"
)

// Doc is a document to index.
type Doc struct {
	ID   int
	Text string
}

// BuildIndex maps each lower-cased word to the documents containing it.
func BuildIndex(docs []Doc) map[string][]int {
	index := map[string][]int{}
	for _, d := range docs {
		for _, w := range strings.Fields(d.Text) {
			word := strings.ToLower(w)
			index[word] = append(index[word], d.ID)
		}
	}
	return index
}

// Summaries renders one line per document.
func Summaries(docs []Doc) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		words := strings.Fields(d.Text)
		out = append(out, "doc "+strconv.Itoa(d.ID)+": "+strconv.Itoa(len(words))+" words, first="+words[0])
	}
	return out
}
