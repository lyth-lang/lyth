// Package goimpl is the hand-written Go version of the pgapi (PostgreSQL)
// workload.
package goimpl

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when no user has the requested id.
var ErrNotFound = errors.New("user not found")

// User is a row of bench_users.
type User struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Email *string `json:"email,omitempty"`
}

// FetchJSON loads a user and encodes it as JSON.
func FetchJSON(ctx context.Context, pool *pgxpool.Pool, id int64) ([]byte, error) {
	var u User
	err := pool.QueryRow(ctx, "select id, name, email from bench_users where id = $1", id).Scan(&u.ID, &u.Name, &u.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(u)
}
