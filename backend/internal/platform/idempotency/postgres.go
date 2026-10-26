package idempotency

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is the durable idempotency store backed by the
// idempotency_keys table.
type PostgresStore struct {
	pool *pgxpool.Pool
}

var _ Store = (*PostgresStore)(nil)

// NewPostgresStore returns a store backed by pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const cleanupExpiredSQL = `DELETE FROM idempotency_keys WHERE expires_at <= now()`

// getSQL also purges expired records. The SELECT filters expired rows because
// data-modifying CTEs share the statement snapshot.
const getSQL = `WITH expired AS (
	DELETE FROM idempotency_keys WHERE expires_at <= now()
)
SELECT response_status, response_body, request_hash
FROM idempotency_keys
WHERE user_id = $1 AND key = $2 AND method = $3 AND path = $4 AND expires_at > now()`

const saveSQL = `INSERT INTO idempotency_keys
	(key, user_id, method, path, request_hash, response_status, response_body, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, now() + interval '24 hours')
ON CONFLICT (user_id, key, method, path) DO NOTHING`

// Get returns the live stored response for the scope, or (nil, nil) when none
// exists.
func (s *PostgresStore) Get(ctx context.Context, userID int64, key, method, path string) (*StoredResponse, error) {
	var (
		status      int
		body        []byte
		requestHash string
	)
	err := s.pool.QueryRow(ctx, getSQL, userID, key, method, path).Scan(&status, &body, &requestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("idempotency: get stored response: %w", err)
	}
	return &StoredResponse{Status: status, Body: body, RequestHash: requestHash}, nil
}

// Save records a response for the scope. The first saved response wins: an
// existing live row is never overwritten.
func (s *PostgresStore) Save(ctx context.Context, userID int64, key, method, path, requestHash string, status int, body []byte) error {
	// Cleanup must be its own statement. A data-modifying CTE shares the
	// statement snapshot with the INSERT, so the INSERT would still see an
	// expired row and ON CONFLICT DO NOTHING would silently discard the
	// replacement.
	if _, err := s.pool.Exec(ctx, cleanupExpiredSQL); err != nil {
		return fmt.Errorf("idempotency: purge expired records: %w", err)
	}
	var responseBody *string
	if len(body) > 0 {
		value := string(body)
		responseBody = &value
	}
	if _, err := s.pool.Exec(ctx, saveSQL, key, userID, method, path, requestHash, status, responseBody); err != nil {
		return fmt.Errorf("idempotency: save response: %w", err)
	}
	return nil
}
