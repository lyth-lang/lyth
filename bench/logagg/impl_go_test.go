//go:build !impl_lyth

package logagg_test

import "lythbench/logagg/goimpl"

func aggregate(lines []string) (int, error) {
	out, err := goimpl.Aggregate(lines)
	return len(out), err
}
