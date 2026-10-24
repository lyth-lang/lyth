//go:build impl_lyth

package index_test

import "lythbench/index/lythimpl"

type docs = []lythimpl.Doc

func makeDocs(ids []int, texts []string) docs {
	out := make(docs, len(ids))
	for i := range ids {
		out[i] = lythimpl.Doc{ID: ids[i], Text: texts[i]}
	}
	return out
}

func buildIndex(d docs) int     { return len(lythimpl.BuildIndex(d)) }
func summaries(d docs) []string { return lythimpl.Summaries(d) }
