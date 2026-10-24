//go:build impl_lyth

package logagg_test

import "lythbench/logagg/lythimpl"

func aggregate(lines []string) (int, error) {
	out, err := lythimpl.Aggregate(lines)
	return len(out), err
}
