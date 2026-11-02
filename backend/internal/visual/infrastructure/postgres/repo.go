// Package visualpg is the PostgreSQL adapter for the P5 visual bounded context.
// It implements the visual application repository ports over the three frozen
// tables (visual_assertion, visual_baseline, visual_diff). The partial unique
// index and the append-only diff table are the source of truth for idempotency;
// services still validate before writing.
package visualpg

import (
	"context"
	"errors"

	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// base carries the shared pool of every aggregate store.
type base struct {
	pool *pgxpool.Pool
}

// Repository groups the visual aggregate stores.
type Repository struct {
	Assertions *AssertionRepository
	Baselines  *BaselineRepository
	Diffs      *DiffRepository
}

// NewRepository wires the visual stores over one pgx pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	shared := base{pool: pool}
	return &Repository{
		Assertions: &AssertionRepository{shared},
		Baselines:  &BaselineRepository{shared},
		Diffs:      &DiffRepository{shared},
	}
}

// wrapDB maps driver failures onto the visual domain sentinels.
func wrapDB(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return visual.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return visual.ErrDuplicate
		case "23503": // foreign_key_violation
			return visual.ErrStateConflict
		case "23514": // check_violation
			return visual.ErrInvalidConfig
		}
	}
	return errors.Join(errors.New("visual postgres: "+op), err)
}

// contextOrBackground keeps repository calls safe during best-effort event/audit
// bookkeeping where the request context may already be canceled.
func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
