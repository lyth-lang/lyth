//go:build impl_lyth

package collections_test

import "lythbench/collections/lythimpl"

func profiles(n int) ([]lythimpl.Profile, map[int]bool) {
	ids, dists, hidden, blocked := fields(n)
	out := make([]lythimpl.Profile, n)
	for i := range ids {
		out[i] = lythimpl.Profile{ID: ids[i], Distance: dists[i], Hidden: hidden[i]}
	}
	return out, blocked
}

func rank(ps []lythimpl.Profile, blocked map[int]bool, limit int) []lythimpl.Profile {
	return lythimpl.Rank(ps, blocked, limit)
}
func nearIDs(ps []lythimpl.Profile, max int) []int        { return lythimpl.NearIDs(ps, max) }
func byDistance(ps []lythimpl.Profile) []lythimpl.Profile { return lythimpl.ByDistance(ps) }
