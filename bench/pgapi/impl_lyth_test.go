//go:build impl_lyth

package pgapi_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"lythbench/pgapi/lythimpl"
)

func fetchJSON(ctx context.Context, pool *pgxpool.Pool, id int64) ([]byte, error) {
	return lythimpl.FetchJSON(ctx, pool, id)
}
