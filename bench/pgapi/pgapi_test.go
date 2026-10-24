package pgapi_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"lythbench/pgapi/goimpl"
	"lythbench/pgapi/lythimpl"
)

// The PostgreSQL workload is opt-in: set LYTH_BENCH_DATABASE_URL to a
// database where the table bench_users may be created.
func openPool(tb testing.TB) *pgxpool.Pool {
	url := os.Getenv("LYTH_BENCH_DATABASE_URL")
	if url == "" {
		tb.Skip("set LYTH_BENCH_DATABASE_URL to run PostgreSQL benchmarks")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `create table if not exists bench_users (id bigint primary key, name text not null, email text)`)
	if err != nil {
		tb.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from bench_users`).Scan(&n); err != nil {
		tb.Fatal(err)
	}
	if n < 1000 {
		_, err = pool.Exec(ctx, `insert into bench_users
			select i, 'user ' || i, case when i % 3 = 0 then null else 'user' || i || '@example.com' end
			from generate_series(1, 1000) i on conflict do nothing`)
		if err != nil {
			tb.Fatal(err)
		}
	}
	return pool
}

var sinkBytes []byte

func BenchmarkFetchJSON(b *testing.B) {
	pool := openPool(b)
	ctx := context.Background()
	id := int64(0)
	for b.Loop() {
		id = id%1000 + 1
		out, err := fetchJSON(ctx, pool, id)
		if err != nil {
			b.Fatal(err)
		}
		sinkBytes = out
	}
}

func TestEquivalence(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	for _, id := range []int64{1, 3, 999} {
		g, gerr := goimpl.FetchJSON(ctx, pool, id)
		n, nerr := lythimpl.FetchJSON(ctx, pool, id)
		if gerr != nil || nerr != nil || !bytes.Equal(g, n) {
			t.Errorf("id %d: go=(%s, %v) lyth=(%s, %v)", id, g, gerr, n, nerr)
		}
	}
	_, gerr := goimpl.FetchJSON(ctx, pool, 5000)
	_, nerr := lythimpl.FetchJSON(ctx, pool, 5000)
	if !errors.Is(gerr, goimpl.ErrNotFound) || !errors.Is(nerr, lythimpl.ErrNotFound) {
		t.Errorf("missing user: go=%v lyth=%v", gerr, nerr)
	}
}
