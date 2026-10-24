package errprop_test

import (
	"errors"
	"fmt"
	"testing"

	"lythbench/errprop/goimpl"
	"lythbench/errprop/lythimpl"
)

var sinkInt int

func BenchmarkChain(b *testing.B) {
	for _, depth := range []int{1, 10} {
		for _, fail := range []bool{false, true} {
			b.Run(fmt.Sprintf("depth=%d/fail=%v", depth, fail), func(b *testing.B) {
				for b.Loop() {
					v, _ := chain(depth, fail)
					sinkInt += v
				}
			})
		}
	}
}

func BenchmarkChainWrapped(b *testing.B) {
	for _, fail := range []bool{false, true} {
		b.Run(fmt.Sprintf("depth=10/fail=%v", fail), func(b *testing.B) {
			for b.Loop() {
				v, _ := chainWrapped(10, fail)
				sinkInt += v
			}
		})
	}
}

func BenchmarkFallback(b *testing.B) {
	for b.Loop() {
		sinkInt += fallback(10, true)
	}
}

// TestEquivalence checks that both implementations behave identically.
func TestEquivalence(t *testing.T) {
	for _, depth := range []int{0, 1, 5} {
		for _, fail := range []bool{false, true} {
			gv, gerr := goimpl.Chain(depth, fail)
			nv, nerr := lythimpl.Chain(depth, fail)
			if gv != nv || (gerr == nil) != (nerr == nil) {
				t.Errorf("Chain(%d, %v): go=(%d, %v) lyth=(%d, %v)", depth, fail, gv, gerr, nv, nerr)
			}
			if gerr != nil && !errors.Is(nerr, lythimpl.ErrBottom) {
				t.Errorf("lyth error %v does not wrap ErrBottom", nerr)
			}
			gw, gwerr := goimpl.ChainWrapped(depth, fail)
			nw, nwerr := lythimpl.ChainWrapped(depth, fail)
			if gw != nw || fmt.Sprint(gwerr) != fmt.Sprint(nwerr) {
				t.Errorf("ChainWrapped(%d, %v): go=(%d, %v) lyth=(%d, %v)", depth, fail, gw, gwerr, nw, nwerr)
			}
			if goimpl.Fallback(depth, fail) != lythimpl.Fallback(depth, fail) {
				t.Errorf("Fallback(%d, %v) differs", depth, fail)
			}
		}
	}
}
