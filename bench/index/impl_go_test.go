//go:build !impl_lyth

package index_test

import "lythbench/index/goimpl"

type docs = []goimpl.Doc

func makeDocs(ids []int, texts []string) docs {
	out := make(docs, len(ids))
	for i := range ids {
		out[i] = goimpl.Doc{ID: ids[i], Text: texts[i]}
	}
	return out
}

func buildIndex(d docs) int     { return len(goimpl.BuildIndex(d)) }
func summaries(d docs) []string { return goimpl.Summaries(d) }
