package logagg_test

import (
	"fmt"
	"testing"

	"lythbench/internal/fixtures"
	"lythbench/logagg/goimpl"
	"lythbench/logagg/lythimpl"
)

var sinkInt int

func BenchmarkAggregate(b *testing.B) {
	lines := fixtures.LogLines(10_000)
	for b.Loop() {
		n, err := aggregate(lines)
		if err != nil {
			b.Fatal(err)
		}
		sinkInt += n
	}
}

func TestEquivalence(t *testing.T) {
	lines := fixtures.LogLines(2_000)
	g, gerr := goimpl.Aggregate(lines)
	n, nerr := lythimpl.Aggregate(lines)
	if gerr != nil || nerr != nil || len(g) != len(n) {
		t.Fatalf("go=(%d, %v) lyth=(%d, %v)", len(g), gerr, len(n), nerr)
	}
	for i := range g {
		if fmt.Sprint(g[i]) != fmt.Sprint(n[i]) {
			t.Errorf("row %d: go=%v lyth=%v", i, g[i], n[i])
		}
	}
	bad := append(lines[:1:1], "garbage")
	_, gerr = goimpl.Aggregate(bad)
	_, nerr = lythimpl.Aggregate(bad)
	if fmt.Sprint(gerr) != fmt.Sprint(nerr) {
		t.Errorf("errors differ: go=%v lyth=%v", gerr, nerr)
	}
}
