// Package goimpl is the hand-written Go version of the collections workload:
// the loops a Go programmer writes instead of lyth/coll chains.
package goimpl

import (
	"cmp"
	"slices"
)

// Profile is a candidate in a ranking.
type Profile struct {
	ID       int
	Distance int
	Hidden   bool
}

// The functions give the same guarantees as their lyth/coll versions: a new,
// right-sized result that shares no storage with the input, never nil.

// Rank returns the nearest limit visible profiles that are not blocked.
func Rank(ps []Profile, blocked map[int]bool, limit int) []Profile {
	out := make([]Profile, 0, len(ps))
	for _, p := range ps {
		if !p.Hidden && !blocked[p.ID] {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(a, b Profile) int { return cmp.Compare(a.Distance, b.Distance) })
	return append([]Profile{}, out[:min(limit, len(out))]...)
}

// NearIDs returns the IDs of the profiles closer than max.
func NearIDs(ps []Profile, max int) []int {
	out := []int{}
	for _, p := range ps {
		if p.Distance < max {
			out = append(out, p.ID)
		}
	}
	return out
}

// ByDistance returns the profiles sorted by distance, leaving ps unchanged.
func ByDistance(ps []Profile) []Profile {
	out := make([]Profile, len(ps))
	copy(out, ps)
	slices.SortStableFunc(out, func(a, b Profile) int { return cmp.Compare(a.Distance, b.Distance) })
	return out
}
