package jsoncodec_test

import (
	"bytes"
	"fmt"
	"testing"

	"lythbench/internal/fixtures"
	"lythbench/jsoncodec/goimpl"
	"lythbench/jsoncodec/lythimpl"
)

var sinkInt64 int64
var sinkBytes []byte

func BenchmarkDecodeTotal(b *testing.B) {
	for _, n := range []int{5, 50} {
		data := fixtures.OrderJSON(n)
		b.Run(fmt.Sprintf("items=%d", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				t, err := decodeTotal(data)
				if err != nil {
					b.Fatal(err)
				}
				sinkInt64 += t
			}
		})
	}
}

func BenchmarkRoundTrip(b *testing.B) {
	data := fixtures.OrderJSON(50)
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		out, err := roundTrip(data)
		if err != nil {
			b.Fatal(err)
		}
		sinkBytes = out
	}
}

func TestEquivalence(t *testing.T) {
	data := fixtures.OrderJSON(7)
	gt, gerr := goimpl.DecodeTotal(data)
	nt, nerr := lythimpl.DecodeTotal(data)
	if gt != nt || gerr != nil || nerr != nil {
		t.Fatalf("DecodeTotal: go=(%d, %v) lyth=(%d, %v)", gt, gerr, nt, nerr)
	}
	gb, _ := goimpl.RoundTrip(data)
	nb, _ := lythimpl.RoundTrip(data)
	if !bytes.Equal(gb, nb) {
		t.Fatalf("RoundTrip differs:\ngo:  %s\nlyth: %s", gb, nb)
	}
	bad := []byte(`{"id":1,"items":[{"sku":"x","quantity":0,"priceCents":1}]}`)
	_, gerr = goimpl.DecodeTotal(bad)
	_, nerr = lythimpl.DecodeTotal(bad)
	if fmt.Sprint(gerr) != fmt.Sprint(nerr) {
		t.Fatalf("validation errors differ: go=%v lyth=%v", gerr, nerr)
	}
}
