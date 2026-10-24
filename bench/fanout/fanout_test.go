package fanout_test

import (
	"context"
	"errors"
	"testing"

	"lythbench/fanout/goimpl"
	"lythbench/fanout/lythimpl"
	"lythbench/internal/fixtures"
)

var sinkInt64 int64

func BenchmarkProcessAll(b *testing.B) {
	items := fixtures.Items(2_000)
	ctx := context.Background()
	b.Run("ok", func(b *testing.B) {
		for b.Loop() {
			v, err := processAll(ctx, items, 16, -1)
			if err != nil {
				b.Fatal(err)
			}
			sinkInt64 += v
		}
	})
	b.Run("fail-early", func(b *testing.B) {
		for b.Loop() {
			_, err := processAll(ctx, items, 16, 100)
			if err == nil {
				b.Fatal("expected error")
			}
		}
	})
}

func TestEquivalence(t *testing.T) {
	items := fixtures.Items(500)
	ctx := context.Background()
	g, gerr := goimpl.ProcessAll(ctx, items, 8, -1)
	n, nerr := lythimpl.ProcessAll(ctx, items, 8, -1)
	if g != n || gerr != nil || nerr != nil {
		t.Fatalf("go=(%d, %v) lyth=(%d, %v)", g, gerr, n, nerr)
	}
	_, gerr = goimpl.ProcessAll(ctx, items, 8, 10)
	_, nerr = lythimpl.ProcessAll(ctx, items, 8, 10)
	if !errors.Is(gerr, goimpl.ErrItem) || !errors.Is(nerr, lythimpl.ErrItem) {
		t.Fatalf("failure: go=%v lyth=%v", gerr, nerr)
	}
}
