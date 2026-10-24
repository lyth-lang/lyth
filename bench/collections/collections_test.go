package collections_test

import (
	"fmt"
	"reflect"
	"testing"

	"lythbench/collections/goimpl"
	"lythbench/collections/lythimpl"
)

var sizes = []int{100, 1_000, 10_000, 100_000}

// fields generates n deterministic profiles: distances spread over 0..10006,
// every tenth one hidden, and blocked holds every hundredth ID.
func fields(n int) (ids, dists []int, hidden []bool, blocked map[int]bool) {
	blocked = map[int]bool{}
	for i := range n {
		ids = append(ids, i)
		dists = append(dists, (i*7919)%10007)
		hidden = append(hidden, i%10 == 0)
		if i%100 == 7 {
			blocked[i] = true
		}
	}
	return ids, dists, hidden, blocked
}

var sinkLen int

// BenchmarkRank: Filter (about 89% kept) -> SortedBy -> Take(20).
func BenchmarkRank(b *testing.B) {
	for _, n := range sizes {
		ps, blocked := profiles(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			for b.Loop() {
				sinkLen += len(rank(ps, blocked, 20))
			}
		})
	}
}

// BenchmarkNearIDs: Filter (about half kept) -> Map.
func BenchmarkNearIDs(b *testing.B) {
	for _, n := range sizes {
		ps, _ := profiles(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			for b.Loop() {
				sinkLen += len(nearIDs(ps, 5000))
			}
		})
	}
}

// BenchmarkByDistance: a sorted copy.
func BenchmarkByDistance(b *testing.B) {
	for _, n := range sizes {
		ps, _ := profiles(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			for b.Loop() {
				sinkLen += len(byDistance(ps))
			}
		})
	}
}

func TestEquivalence(t *testing.T) {
	ids, dists, hidden, blocked := fields(2_000)
	gp := make([]goimpl.Profile, len(ids))
	np := make([]lythimpl.Profile, len(ids))
	for i := range ids {
		gp[i] = goimpl.Profile{ID: ids[i], Distance: dists[i], Hidden: hidden[i]}
		np[i] = lythimpl.Profile{ID: ids[i], Distance: dists[i], Hidden: hidden[i]}
	}
	sameIDs := func(what string, g []goimpl.Profile, n []lythimpl.Profile) {
		if len(g) != len(n) {
			t.Fatalf("%s: %d vs %d profiles", what, len(g), len(n))
		}
		for i := range g {
			if g[i].ID != n[i].ID {
				t.Fatalf("%s: profile %d differs: %d vs %d", what, i, g[i].ID, n[i].ID)
			}
		}
	}
	sameIDs("Rank", goimpl.Rank(gp, blocked, 20), lythimpl.Rank(np, blocked, 20))
	sameIDs("ByDistance", goimpl.ByDistance(gp), lythimpl.ByDistance(np))
	if !reflect.DeepEqual(goimpl.NearIDs(gp, 5000), lythimpl.NearIDs(np, 5000)) {
		t.Error("NearIDs differ")
	}
	// Both give right-sized results, and empty results that are not nil.
	gr, nr := goimpl.Rank(gp, blocked, 20), lythimpl.Rank(np, blocked, 20)
	if cap(gr) != len(gr) || cap(nr) != len(nr) {
		t.Errorf("Rank keeps a larger buffer: cap %d/%d, len %d/%d", cap(gr), cap(nr), len(gr), len(nr))
	}
	if goimpl.NearIDs(gp, 0) == nil || lythimpl.NearIDs(np, 0) == nil || goimpl.ByDistance(nil) == nil || lythimpl.ByDistance(nil) == nil {
		t.Error("an empty result is nil")
	}
	// Neither version changes its input.
	for i := range np {
		if np[i].ID != i || gp[i].ID != i {
			t.Fatal("input reordered")
		}
	}
}
