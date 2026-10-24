//go:build !impl_lyth

package fanout_test

import (
	"context"

	"lythbench/fanout/goimpl"
)

func processAll(ctx context.Context, items []string, limit, failAt int) (int64, error) {
	return goimpl.ProcessAll(ctx, items, limit, failAt)
}
