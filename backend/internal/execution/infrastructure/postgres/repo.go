// Package executionpg is the PostgreSQL adapter for the P3 execution bounded
// context. It implements the application repository ports with explicit SQL,
// partial-unique idempotency guard expectations and dispatch-epoch/token
// fencing for late writes.
//
// P3 assumes a single API instance; the repositories still enforce the database
// invariants (partial unique indexes, CAS guards, single active wave).
//
// Each aggregate has its own store type so the same verb (Create/Get/Update)
// can implement the narrow application interfaces without name collisions.
// Repository groups them for the assembly root.
package executionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// base carries the shared pool/ID generator of every aggregate store.
type base struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// Repository groups the execution aggregates. The application services take the
// narrow store interfaces (IntentRepository, WaveRepository, ...) rather than
// this aggregate.
type Repository struct {
	Intents     *IntentRepository
	Waves       *WaveRepository
	CaseRuns    *CaseRunRepository
	SUTContexts *SUTContextRepository
	Results     *ResultRepository
	Evidence    *EvidenceRepository
	Executors   *ExecutorRepository
	Humans      *HumanRequestRepository
}

// NewRepository wires every execution aggregate store over one pgx pool. ids
// must be the shared snowflake generator of the process.
func NewRepository(pool *pgxpool.Pool, ids platformid.Generator) *Repository {
	shared := base{pool: pool, ids: ids}
	return &Repository{
		Intents:     &IntentRepository{shared},
		Waves:       &WaveRepository{shared},
		CaseRuns:    &CaseRunRepository{shared},
		SUTContexts: &SUTContextRepository{shared},
		Results:     &ResultRepository{shared},
		Evidence:    &EvidenceRepository{shared},
		Executors:   &ExecutorRepository{shared},
		Humans:      &HumanRequestRepository{shared},
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

// wrapDB maps shared PostgreSQL failures onto the execution domain sentinels.
func wrapDB(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return execution.ErrDuplicate
		case "23503": // foreign_key_violation
			return execution.ErrNotFound
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

// isNoRows reports pgx's empty-result sentinel.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// errFenceMismatch marks a stale dispatch epoch/token write. The application
// maps it onto 409 lease_lost.
var errFenceMismatch = errors.New("execution dispatch fence mismatch")

// fenceCaseRun verifies the case_run still carries the expected dispatch
// epoch/token. A zero epoch or empty token means "no fence".
func fenceCaseRun(ctx context.Context, tx pgx.Tx, caseRunID, epoch int64, token string) error {
	var rowEpoch int64
	var rowToken string
	if err := tx.QueryRow(ctx, `
		SELECT dispatch_epoch, dispatch_token FROM case_run WHERE id = $1
	`, caseRunID).Scan(&rowEpoch, &rowToken); err != nil {
		return wrapDB("fence case run", err)
	}
	if epoch > 0 && rowEpoch != epoch {
		return errFenceMismatch
	}
	if token != "" && rowToken != token {
		return errFenceMismatch
	}
	return nil
}

// insertID returns the explicit ID when set, otherwise a generated snowflake.
func (b base) insertID(explicit int64) int64 {
	if explicit > 0 {
		return explicit
	}
	return b.ids.Next()
}

// existsOrNotFound reports nil when the row exists (a guarded no-op) and
// ErrNotFound when the row is missing.
func (b base) existsOrNotFound(ctx context.Context, table string, id int64, op string) error {
	var found int
	err := b.pool.QueryRow(ctx, `SELECT 1 FROM `+table+` WHERE id = $1`, id).Scan(&found)
	if err != nil {
		if isNoRows(err) {
			return execution.ErrNotFound
		}
		return wrapDB(op, err)
	}
	return nil
}

// encodeJSONObject renders a JSONB value, defaulting to {}.
func encodeJSONObject(field string, value any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", field, err)
	}
	if string(encoded) == "null" {
		return "{}", nil
	}
	return string(encoded), nil
}

// encodeJSONArray renders a JSONB array, defaulting to [].
func encodeJSONArray(field string, value any) (string, error) {
	if value == nil {
		return "[]", nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", field, err)
	}
	if string(encoded) == "null" {
		return "[]", nil
	}
	return string(encoded), nil
}

// decodeJSONObject decodes a JSONB object column into out.
func decodeJSONObject(field string, raw []byte, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s: %w", field, err)
	}
	return nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// prefixedColumns prefixes a comma/newline separated column list with an alias.
func prefixedColumns(alias, columns string) string {
	result := make([]string, 0)
	current := make([]rune, 0)
	flush := func() {
		value := trimColumn(string(current))
		if value != "" {
			result = append(result, value)
		}
		current = current[:0]
	}
	for _, char := range columns {
		switch char {
		case ',', '\n', '\t', '\r':
			flush()
		default:
			current = append(current, char)
		}
	}
	flush()
	prefixed := ""
	for i, column := range result {
		if i > 0 {
			prefixed += ", "
		}
		prefixed += alias + "." + column
	}
	return prefixed
}

func trimColumn(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\n' || value[start] == '\t' || value[start] == '\r') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\n' || value[end-1] == '\t' || value[end-1] == '\r') {
		end--
	}
	return value[start:end]
}

func effectiveLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func fetchLimit(limit int) int { return effectiveLimit(limit) + 1 }

func statusStrings[T ~string](values []T) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func nullableID(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
