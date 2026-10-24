// Package goimpl is the hand-written Go version of the errprop workload.
package goimpl

import (
	"errors"
	"fmt"
)

// ErrBottom is returned by the deepest call when it fails.
var ErrBottom = errors.New("bottom")

func level(n int, fail bool) (int, error) {
	if n == 0 {
		if fail {
			return 0, ErrBottom
		}
		return 1, nil
	}
	v, err := level(n-1, fail)
	if err != nil {
		return 0, err
	}
	return v + 1, nil
}

// Chain propagates an error unchanged through depth calls.
func Chain(depth int, fail bool) (int, error) {
	return level(depth, fail)
}

func levelWrapped(n int, fail bool) (int, error) {
	if n == 0 {
		if fail {
			return 0, ErrBottom
		}
		return 1, nil
	}
	v, err := levelWrapped(n-1, fail)
	if err != nil {
		return 0, fmt.Errorf("level %d: %w", n, err)
	}
	return v + 1, nil
}

// ChainWrapped adds context at every level.
func ChainWrapped(depth int, fail bool) (int, error) {
	return levelWrapped(depth, fail)
}

// Fallback handles the error locally with a default value.
func Fallback(depth int, fail bool) int {
	v, err := level(depth, fail)
	if err != nil {
		return -1
	}
	return v
}
