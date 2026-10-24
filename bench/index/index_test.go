package index_test

import (
	"reflect"
	"testing"

	"lythbench/index/goimpl"
	"lythbench/index/lythimpl"
	"lythbench/internal/fixtures"
)

var sinkInt int
var sinkStrings []string

func BenchmarkBuildIndex(b *testing.B) {
	d := makeDocs(fixtures.Docs(1_000))
	for b.Loop() {
		sinkInt += buildIndex(d)
	}
}

func BenchmarkSummaries(b *testing.B) {
	d := makeDocs(fixtures.Docs(1_000))
	for b.Loop() {
		sinkStrings = summaries(d)
	}
}

func TestEquivalence(t *testing.T) {
	ids, texts := fixtures.Docs(300)
	gd := make([]goimpl.Doc, len(ids))
	nd := make([]lythimpl.Doc, len(ids))
	for i := range ids {
		gd[i] = goimpl.Doc{ID: ids[i], Text: texts[i]}
		nd[i] = lythimpl.Doc{ID: ids[i], Text: texts[i]}
	}
	if !reflect.DeepEqual(goimpl.BuildIndex(gd), lythimpl.BuildIndex(nd)) {
		t.Error("indexes differ")
	}
	if !reflect.DeepEqual(goimpl.Summaries(gd), lythimpl.Summaries(nd)) {
		t.Error("summaries differ")
	}
}
