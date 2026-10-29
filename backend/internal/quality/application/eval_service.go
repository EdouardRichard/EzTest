// Package application implements the quality use cases: recorded evaluation
// runs, the metric thresholds/regression rule (delegated to the domain) and the
// read model consumed by the admin dashboard and the Agent centre. The eval
// runner drives this package only through the admin HTTP API (spec §9.1).
package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// Service is the quality application service.
type Service struct {
	repo  RunRepository
	ids   IDGenerator
	clock Clock
}

// Option customises the service dependencies.
type Option func(*Service)

// WithIDGenerator sets the Snowflake generator.
func WithIDGenerator(ids IDGenerator) Option { return func(s *Service) { s.ids = ids } }

// WithClock sets the clock.
func WithClock(clock Clock) Option { return func(s *Service) { s.clock = clock } }

// NewService returns the quality service. A nil repository disables writes and
// reads; the composition root always wires one.
func NewService(repo RunRepository, opts ...Option) *Service {
	service := &Service{repo: repo}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

var _ EvalRun = (*Service)(nil)

// Record evaluates one corpus observation, persists the terminal run with its
// metrics and returns the stored aggregate.
func (s *Service) Record(ctx context.Context, input RecordInput) (domain.Run, error) {
	if s == nil || s.repo == nil {
		return domain.Run{}, errors.New("quality: eval run repository is not configured")
	}
	if s.ids == nil {
		return domain.Run{}, errors.New("quality: id generator is not configured")
	}

	mode := input.Mode
	if !mode.IsValid() {
		return domain.Run{}, &ValidationError{Field: "mode", Message: "mode must be deterministic or real_llm"}
	}
	corpusVersion := strings.TrimSpace(input.CorpusVersion)
	if corpusVersion == "" {
		return domain.Run{}, &ValidationError{Field: "corpusVersion", Message: "corpusVersion is required"}
	}
	if len(corpusVersion) > MaxCorpusVersionLength {
		return domain.Run{}, &ValidationError{
			Field:   "corpusVersion",
			Message: fmt.Sprintf("corpusVersion must be at most %d characters", MaxCorpusVersionLength),
		}
	}
	model := strings.TrimSpace(input.Model)
	if len(model) > MaxModelLength {
		return domain.Run{}, &ValidationError{
			Field:   "model",
			Message: fmt.Sprintf("model must be at most %d characters", MaxModelLength),
		}
	}

	outcome := input.Outcome
	if outcome != "" && outcome != domain.StatusError {
		return domain.Run{}, &ValidationError{Field: "status", Message: "status must be omitted or error"}
	}

	startedAt := input.StartedAt
	finishedAt := input.FinishedAt
	if startedAt.IsZero() {
		startedAt = s.now()
	}
	if finishedAt.IsZero() {
		finishedAt = s.now()
	}
	if finishedAt.Before(startedAt) {
		return domain.Run{}, &ValidationError{Field: "finishedAt", Message: "finishedAt must not precede startedAt"}
	}
	startedAt = startedAt.UTC()
	finishedAt = finishedAt.UTC()

	run := domain.Run{
		ID:            s.ids.Next(),
		Mode:          mode,
		CorpusVersion: corpusVersion,
		Model:         model,
		Summary:       copySummary(input.Summary),
		StartedAt:     startedAt,
		FinishedAt:    &finishedAt,
	}
	if outcome == domain.StatusError {
		run.Status = domain.StatusError
	} else {
		if input.Sample == nil {
			return domain.Run{}, &ValidationError{Field: "sample", Message: "sample is required unless status is error"}
		}
		sample := *input.Sample
		if err := sample.ValidateFields(); err != nil {
			var invalid *domain.SampleFieldError
			if errors.As(err, &invalid) {
				return domain.Run{}, &ValidationError{Field: invalid.Field, Message: invalid.Message}
			}
			return domain.Run{}, &ValidationError{Field: "sample", Message: err.Error()}
		}
		run.Metrics = domain.Evaluate(sample, mode, input.Baseline)
		run.Status = domain.StatusPassed
		if mode == domain.ModeRealLLM {
			run.Summary["report_only"] = true
		}
		for _, metric := range run.Metrics {
			if !metric.Passed {
				run.Status = domain.StatusFailed
				break
			}
		}
	}
	if run.Summary == nil {
		run.Summary = map[string]any{}
	}
	run.Summary["passed"] = run.Status == domain.StatusPassed
	run.Summary["metricCount"] = len(run.Metrics)

	created, err := s.repo.Create(ctx, run)
	if err != nil {
		return domain.Run{}, fmt.Errorf("quality: persist evaluation run: %w", err)
	}
	if created.Metrics == nil {
		created.Metrics = run.Metrics
	}
	if created.Summary == nil {
		created.Summary = run.Summary
	}
	return created, nil
}

// Get returns one run or ErrRunNotFound.
func (s *Service) Get(ctx context.Context, id int64) (domain.Run, error) {
	if s == nil || s.repo == nil {
		return domain.Run{}, errors.New("quality: eval run repository is not configured")
	}
	if id <= 0 {
		return domain.Run{}, &ValidationError{Field: "id", Message: "id must be a positive decimal id"}
	}
	run, err := s.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			return domain.Run{}, ErrRunNotFound
		}
		return domain.Run{}, fmt.Errorf("quality: load evaluation run: %w", err)
	}
	return run, nil
}

// List returns the newest runs first. A zero limit selects DefaultListLimit; a
// negative or above-max limit is rejected.
func (s *Service) List(ctx context.Context, limit int) ([]domain.Run, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("quality: eval run repository is not configured")
	}
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaxListLimit {
		return nil, &ValidationError{
			Field:   "limit",
			Message: fmt.Sprintf("limit must be between 1 and %d", MaxListLimit),
		}
	}
	runs, err := s.repo.List(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("quality: list evaluation runs: %w", err)
	}
	if runs == nil {
		runs = []domain.Run{}
	}
	return runs, nil
}

// Latest returns the newest run, or ErrRunNotFound when the platform has never
// been evaluated.
func (s *Service) Latest(ctx context.Context) (domain.Run, error) {
	if s == nil || s.repo == nil {
		return domain.Run{}, errors.New("quality: eval run repository is not configured")
	}
	run, err := s.repo.Latest(ctx)
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			return domain.Run{}, ErrRunNotFound
		}
		return domain.Run{}, fmt.Errorf("quality: load latest evaluation run: %w", err)
	}
	return run, nil
}

// now returns the injected clock time, falling back to the wall clock.
func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}

// copySummary clones the request summary so the service never mutates the
// caller's document and a nil summary becomes an empty JSON object.
func copySummary(input map[string]any) map[string]any {
	summary := make(map[string]any, len(input)+2)
	for key, value := range input {
		summary[key] = value
	}
	return summary
}
