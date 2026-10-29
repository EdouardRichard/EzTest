package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// BaselineDeps are the ports of the baseline freeze use case: the transaction
// runner and the graph repositories it snapshots, the review pair used to bind
// the baseline to its approved review, the version lifecycle write port, the id
// generator and the clock.
type BaselineDeps struct {
	Graph      GraphRepository
	Nodes      NodeRepository
	Steps      StepRepository
	Assertions AssertionRepository
	Sources    SourceRepository
	Reviews    ReviewRepository
	Baselines  BaselineRepository
	Versions   VersionLifecycle
	IDs        IDGenerator
	Clock      Clock
}

// BaselineService freezes the reviewed mindmap of a version (spec §6.4). The
// baseline row and the draft→reviewed node transition are written in one
// transaction; the version transition runs after the commit and is compensated
// when it loses the CAS race.
type BaselineService struct {
	deps BaselineDeps
}

// NewBaselineService wires the freeze use case to its ports.
func NewBaselineService(deps BaselineDeps) *BaselineService {
	return &BaselineService{deps: deps}
}

// Freeze snapshots the graph of versionID into case_baseline and moves every
// draft node to reviewed, then asks the version lifecycle to approve the
// baseline (spec §6.4 order: baseline + nodes first, version last).
//
// When ApproveBaseline fails the write is compensated in a second transaction:
// the baseline is deleted and every node is restored to its pre-freeze status and
// revision. A failed compensation is returned as a non-domain error so the HTTP
// layer reports an internal failure, as the spec requires.
//
// Freezing a version that already has a baseline is the stable 409
// baseline_frozen; the baseline row is unique per version (D5).
func (s *BaselineService) Freeze(ctx context.Context, versionID, actor int64) (domain.CaseBaseline, error) {
	if err := s.validate(); err != nil {
		return domain.CaseBaseline{}, err
	}
	existing, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		return domain.CaseBaseline{}, err
	}
	if existing != nil {
		return domain.CaseBaseline{}, baselineFrozenError(versionID)
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return domain.CaseBaseline{}, err
	}
	if version.Status != versionStatusReviewing {
		return domain.CaseBaseline{}, reviewVersionStateError(versionID, version.Status)
	}
	review, err := s.approvedReview(ctx, versionID)
	if err != nil {
		return domain.CaseBaseline{}, err
	}
	id, err := s.nextID()
	if err != nil {
		return domain.CaseBaseline{}, err
	}
	frozenAt := s.now()

	// The snapshot, the canonical hash, the counts and the pre-freeze rows are
	// all read inside the freeze transaction (spec §6.4): a concurrent edit
	// committed before the transaction must be part of the frozen baseline, and
	// the compensation needs the exact rows the transaction saw.
	var frozen domain.CaseBaseline
	var preFreeze []domain.CaseNode
	err = s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if tx.Baselines() == nil || tx.Nodes() == nil {
			return errors.New("mindmap: baseline transaction repositories are not configured")
		}
		// Re-check the unique baseline inside the transaction so two concurrent
		// freezes cannot both write: the loser reports baseline_frozen and rolls
		// back without touching the node status.
		current, err := loadBaseline(ctx, tx.Baselines(), versionID)
		if err != nil {
			return err
		}
		if current != nil {
			return baselineFrozenError(versionID)
		}
		snapshot, err := loadSnapshotTx(ctx, tx, versionID)
		if err != nil {
			return err
		}
		preFreeze = append([]domain.CaseNode(nil), snapshot.Nodes...)
		baseline := domain.CaseBaseline{
			ID:                 id,
			VersionID:          versionID,
			ReviewID:           review.ID,
			NodeCount:          len(snapshot.Nodes),
			CaseCount:          baselineCaseCount(snapshot.Nodes),
			GraphHash:          domain.CanonicalGraphHash(snapshot),
			CodeBaselineCommit: version.CodeBaselineCommit,
			FrozenBy:           actor,
			FrozenAt:           frozenAt,
		}
		stored, err := tx.Baselines().Create(ctx, baseline)
		if err != nil {
			return err
		}
		if _, err := tx.Nodes().UpdateStatusByVersion(ctx, versionID,
			domain.NodeStatusDraft, domain.NodeStatusReviewed); err != nil {
			return err
		}
		frozen = stored
		return nil
	})
	if err != nil {
		return domain.CaseBaseline{}, err
	}
	if _, err := s.deps.Versions.ApproveBaseline(ctx, versionID, actor); err != nil {
		return domain.CaseBaseline{}, s.compensate(ctx, versionID, preFreeze, err)
	}
	return frozen, nil
}

// validate reports the mandatory freeze ports.
func (s *BaselineService) validate() error {
	if s.deps.Graph == nil || s.deps.Nodes == nil || s.deps.Steps == nil ||
		s.deps.Assertions == nil || s.deps.Sources == nil || s.deps.Reviews == nil ||
		s.deps.Baselines == nil || s.deps.Versions == nil || s.deps.IDs == nil {
		return errors.New("mindmap: baseline service ports are not configured")
	}
	return nil
}

// loadSnapshotTx reads the whole graph of versionID through the transaction's
// repositories, so the hash and the counts describe exactly the rows the freeze
// transaction commits.
func loadSnapshotTx(ctx context.Context, tx GraphTx, versionID int64) (domain.GraphSnapshot, error) {
	state, err := loadGraphStateTx(ctx, tx, versionID)
	if err != nil {
		return domain.GraphSnapshot{}, err
	}
	return domain.GraphSnapshot{
		VersionID:  versionID,
		Nodes:      state.nodes,
		Steps:      state.steps,
		Assertions: state.assertions,
		Sources:    state.sources,
	}, nil
}

// approvedReview returns the approved review the baseline belongs to. The review
// service approves the latest draft review before freezing, so a missing or
// unapproved review means the caller skipped that step.
func (s *BaselineService) approvedReview(ctx context.Context, versionID int64) (domain.CaseReview, error) {
	if s.deps.Reviews == nil {
		return domain.CaseReview{}, errors.New("mindmap: review repository is not configured")
	}
	review, err := s.deps.Reviews.GetLatest(ctx, versionID)
	if err != nil {
		if reviewIsNotFound(err) {
			return domain.CaseReview{}, domain.NewNotFound("review", versionID)
		}
		return domain.CaseReview{}, err
	}
	if review.Status != domain.ReviewApproved {
		return domain.CaseReview{}, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("review %d of version %d is %s; approve it before freezing", review.ID, versionID, review.Status))
	}
	return review, nil
}

// compensate undoes a committed freeze whose ApproveBaseline call failed: the
// baseline row is deleted and every pre-freeze node is restored with its exact
// status and revision. It must open its own transaction because the freeze
// transaction already committed (spec §6.4).
func (s *BaselineService) compensate(ctx context.Context, versionID int64, preFreeze []domain.CaseNode, cause error) error {
	compensationErr := s.deps.Graph.InTx(ctx, func(tx GraphTx) error {
		if tx.Baselines() == nil || tx.Nodes() == nil {
			return errors.New("mindmap: baseline compensation repositories are not configured")
		}
		if err := tx.Baselines().DeleteByVersion(ctx, versionID); err != nil {
			return err
		}
		if len(preFreeze) == 0 {
			return nil
		}
		return tx.Nodes().RestoreStatusByVersion(ctx, versionID, preFreeze)
	})
	if compensationErr != nil {
		// The graph is left half-frozen (baseline + reviewed nodes with the
		// version still reviewing): this needs manual repair, so it is logged at
		// error level and returned as a non-domain internal failure (spec §6.4).
		slog.ErrorContext(ctx, "mindmap: baseline freeze compensation failed",
			"version_id", versionID, "cause", cause, "error", compensationErr)
		return fmt.Errorf("mindmap: baseline freeze of version %d failed (%v) and its compensation failed: %w",
			versionID, cause, compensationErr)
	}
	return domain.NewStateConflict("version_state_conflict",
		fmt.Sprintf("version %d could not move to executing: %v", versionID, cause))
}

// baselineCaseCount counts the leaf case nodes of a snapshot.
func baselineCaseCount(nodes []domain.CaseNode) int {
	count := 0
	for _, node := range nodes {
		if node.Kind == domain.NodeKindCase {
			count++
		}
	}
	return count
}

// nextID mints one application-generated Snowflake id (D29).
func (s *BaselineService) nextID() (int64, error) {
	if s.deps.IDs == nil {
		return 0, errors.New("mindmap: id generator is not configured")
	}
	id := s.deps.IDs.Next()
	if id <= 0 {
		return 0, errors.New("mindmap: id generator returned a non-positive id")
	}
	return id, nil
}

// now returns the injected clock time (falling back to the wall clock).
func (s *BaselineService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now()
}
