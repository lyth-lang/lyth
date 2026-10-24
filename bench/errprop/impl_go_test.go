//go:build !impl_lyth

package errprop_test

import "lythbench/errprop/goimpl"

func chain(depth int, fail bool) (int, error)        { return goimpl.Chain(depth, fail) }
func chainWrapped(depth int, fail bool) (int, error) { return goimpl.ChainWrapped(depth, fail) }
func fallback(depth int, fail bool) int              { return goimpl.Fallback(depth, fail) }
