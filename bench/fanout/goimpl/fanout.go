// Package goimpl is the hand-written Go version of the fanout (concurrency)
// workload.
package goimpl

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
)

// ErrItem is returned for the item at index failAt.
var ErrItem = errors.New("item failed")

func work(s string) byte {
	b := []byte(s)
	var sum [32]byte
	for range 10 {
		sum = sha256.Sum256(b)
		b = sum[:]
	}
	return sum[0]
}

// ProcessAll hashes every item with at most limit goroutines. The first
// failure cancels the remaining work.
func ProcessAll(ctx context.Context, items []string, limit, failAt int) (int64, error) {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)
	var total atomic.Int64
	for i, item := range items {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			if i == failAt {
				return ErrItem
			}
			total.Add(int64(work(item)))
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return 0, err
	}
	return total.Load(), nil
}
