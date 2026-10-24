//go:build !impl_lyth

package collections_test

import "lythbench/collections/goimpl"

func profiles(n int) ([]goimpl.Profile, map[int]bool) {
	ids, dists, hidden, blocked := fields(n)
	out := make([]goimpl.Profile, n)
	for i := range ids {
		out[i] = goimpl.Profile{ID: ids[i], Distance: dists[i], Hidden: hidden[i]}
	}
	return out, blocked
}

func rank(ps []goimpl.Profile, blocked map[int]bool, limit int) []goimpl.Profile {
	return goimpl.Rank(ps, blocked, limit)
}
func nearIDs(ps []goimpl.Profile, max int) []int      { return goimpl.NearIDs(ps, max) }
func byDistance(ps []goimpl.Profile) []goimpl.Profile { return goimpl.ByDistance(ps) }
