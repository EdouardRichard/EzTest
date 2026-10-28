package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
)

// GraphTx starts graph write transactions on a pgx pool. The callback receives
// the transaction-bound repository view declared by application.GraphTx, so
// materialization (T10) and baseline freezing (T11) can write one whole graph in
// a single transaction. It also implements application.GraphRepository.
type GraphTx struct {
	pool *pgxpool.Pool
}

// NewGraphTx returns the graph transaction runner of pool.
func NewGraphTx(pool *pgxpool.Pool) *GraphTx {
	return &GraphTx{pool: pool}
}

var _ application.GraphRepository = (*GraphTx)(nil)

// InTx begins a transaction, runs fn with every repository bound to it, and
// commits. A callback error rolls the whole transaction back and is returned
// unwrapped so its *domain.Error classification survives. The callback must not
// publish events: events are published only after the commit.
func (g *GraphTx) InTx(ctx context.Context, fn func(application.GraphTx) error) error {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin graph transaction: %w", err)
	}
	defer func() {
		// A commit already closed the transaction; this rollback is then a
		// no-op. WithoutCancel keeps the cleanup working when the caller's
		// context was cancelled.
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()
	if err := fn(newGraphTxRepositories(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit graph transaction: %w", err)
	}
	return nil
}

// graphTxRepositories binds the nine graph repositories to one pgx.Tx.
type graphTxRepositories struct {
	nodes      *NodeRepository
	steps      *StepRepository
	assertions *AssertionRepository
	sources    *SourceRepository
	reviews    *ReviewRepository
	findings   *FindingRepository
	baselines  *BaselineRepository
	runs       *RunRepository
}

// newGraphTxRepositories binds every graph repository to tx.
func newGraphTxRepositories(tx pgx.Tx) *graphTxRepositories {
	return &graphTxRepositories{
		nodes:      newNodeRepository(tx),
		steps:      newStepRepository(tx),
		assertions: newAssertionRepository(tx),
		sources:    newSourceRepository(tx),
		reviews:    newReviewRepository(tx),
		findings:   newFindingRepository(tx),
		baselines:  newBaselineRepository(tx),
		runs:       newRunRepository(tx),
	}
}

var _ application.GraphTx = (*graphTxRepositories)(nil)

// Nodes returns the transaction-bound case-node repository.
func (t *graphTxRepositories) Nodes() application.NodeRepository { return t.nodes }

// Steps returns the transaction-bound case-step repository.
func (t *graphTxRepositories) Steps() application.StepRepository { return t.steps }

// Assertions returns the transaction-bound case-assertion repository.
func (t *graphTxRepositories) Assertions() application.AssertionRepository { return t.assertions }

// Sources returns the transaction-bound case-source repository.
func (t *graphTxRepositories) Sources() application.SourceRepository { return t.sources }

// Reviews returns the transaction-bound case-review repository.
func (t *graphTxRepositories) Reviews() application.ReviewRepository { return t.reviews }

// Findings returns the transaction-bound review-finding repository.
func (t *graphTxRepositories) Findings() application.FindingRepository { return t.findings }

// Baselines returns the transaction-bound case-baseline repository.
func (t *graphTxRepositories) Baselines() application.BaselineRepository { return t.baselines }

// Runs returns the transaction-bound design-run repository.
func (t *graphTxRepositories) Runs() application.RunRepository { return t.runs }
