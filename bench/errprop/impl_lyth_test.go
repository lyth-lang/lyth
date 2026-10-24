//go:build impl_lyth

package errprop_test

import "lythbench/errprop/lythimpl"

func chain(depth int, fail bool) (int, error)        { return lythimpl.Chain(depth, fail) }
func chainWrapped(depth int, fail bool) (int, error) { return lythimpl.ChainWrapped(depth, fail) }
func fallback(depth int, fail bool) int              { return lythimpl.Fallback(depth, fail) }
