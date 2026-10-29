// Package application implements the quality use cases: recorded evaluation
// runs, the metric thresholds/regression rule (delegated to the domain) and the
// read model consumed by the admin dashboard and the Agent centre. The eval
// runner drives this package only through the admin HTTP API (spec §9.1).
package application

import (
	"context"
	"errors"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// Query bounds of GET /api/quality/eval-runs.
const (
	// DefaultListLimit is the page size when the caller omits `limit`.
	DefaultListLimit = 20
	// MaxListLimit bounds one history page.
	MaxListLimit = 50
	// MaxCorpusVersionLength bounds the corpus version label.
	MaxCorpusVersionLength = 64
	// MaxModelLength bounds the model label.
	MaxModelLength = 128
)

// Sentinel errors. The HTTP layer maps ErrRunNotFound to 404 and every
// *ValidationError to the documented 400 invalid_argument envelope.
var (
	// ErrRunNotFound reports an unknown evaluation run id, or an empty history
	// for Latest.
	ErrRunNotFound = errors.New("quality: evaluation run not found")
)

// ValidationError names the rejected request field.
type ValidationError struct {
	Field   string
	Message string
}

// Error implements error.
func (e *ValidationError) Error() string { return e.Message }

// RunRepository persists eval_run rows and their eval_metric children.
// Create writes the run and every metric atomically. Get returns ErrRunNotFound
// for an unknown id and Latest returns ErrRunNotFound when no run exists.
type RunRepository interface {
	Create(ctx context.Context, run domain.Run) (domain.Run, error)
	Get(ctx context.Context, id int64) (domain.Run, error)
	List(ctx context.Context, limit int) ([]domain.Run, error)
	Latest(ctx context.Context) (domain.Run, error)
}

// IDGenerator mints the Snowflake eval_run id (D29/INV-15).
type IDGenerator interface {
	Next() int64
}

// Clock supplies the run timestamps; tests inject a fixed clock.
type Clock interface {
	Now() time.Time
}

// RecordInput is one terminal evaluation run posted by the runner. Outcome may
// be empty (evaluate the sample) or StatusError (the scenario could not run, so
// no metric is fabricated). Sample is required unless the outcome is an error.
// Baseline is the committed deterministic baseline read by the runner; the
// service compares it to the computed metrics so the gate rule lives here.
type RecordInput struct {
	Mode          domain.Mode
	CorpusVersion string
	Model         string
	Outcome       domain.Status
	Sample        *domain.Sample
	Baseline      map[string]float64
	Summary       map[string]any
	StartedAt     time.Time
	FinishedAt    time.Time
}

// EvalRun is the read-only service surface the HTTP handlers consume.
type EvalRun interface {
	Record(ctx context.Context, input RecordInput) (domain.Run, error)
	Get(ctx context.Context, id int64) (domain.Run, error)
	List(ctx context.Context, limit int) ([]domain.Run, error)
	Latest(ctx context.Context) (domain.Run, error)
}
