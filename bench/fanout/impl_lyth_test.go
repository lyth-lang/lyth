//go:build impl_lyth

package fanout_test

import (
	"context"

	"lythbench/fanout/lythimpl"
)

func processAll(ctx context.Context, items []string, limit, failAt int) (int64, error) {
	return lythimpl.ProcessAll(ctx, items, limit, failAt)
}
