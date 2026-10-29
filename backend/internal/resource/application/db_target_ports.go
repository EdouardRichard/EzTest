package application

// The db target ports consumed by the resource application layer. Adapters live
// in infrastructure (PostgreSQL repository, read-only pgx querier).

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// DBTargetRepository is the persistence port of the environment db targets.
// Get returns (nil, nil) when the environment has no target configured so the
// service can distinguish "not configured" from a storage failure.
type DBTargetRepository interface {
	GetDBTarget(ctx context.Context, environmentID int64) (*domain.DBTarget, error)
	// UpsertDBTarget stores target, inserting by id or replacing the row of its
	// environment. The returned row is written back onto target.
	UpsertDBTarget(ctx context.Context, target *domain.DBTarget) error
	// UpdateDBTargetTestResult records the last connection-test outcome only.
	UpdateDBTargetTestResult(ctx context.Context, targetID int64, testedAt time.Time, status, message string) error
	DeleteDBTarget(ctx context.Context, environmentID int64) error
}

// DBTargetProber probes one target connection and reports the server metadata
// and role warnings of spec §5.7. A failed probe is reported in the result, not
// as an error; the error return is reserved for internal failures.
type DBTargetProber interface {
	TestConnection(ctx context.Context, target domain.DBTarget) (domain.DBTargetTestResult, error)
}

// ReadOnlyQuerier executes one validated read-only SQL statement against the
// target (spec §5.7). Implementations must enforce the three read-only layers,
// the static SQL whitelist and the row/byte limits. A failure is a
// *domain.QueryError carrying one of the stable db_* reasons.
type ReadOnlyQuerier interface {
	Query(ctx context.Context, target domain.DBTarget, sql string, limits domain.QueryLimits) (domain.QueryResult, error)
}

// Resource db target audit actions of spec §11.2. Each name is stable because
// it is persisted and filtered on.
const (
	ActionDBTargetUpserted = "resource.db_target.upserted"
	ActionDBTargetTested   = "resource.db_target.tested"
	ActionDBTargetDeleted  = "resource.db_target.deleted"
	// DBTargetAuditTargetType is the audit target type of a db target entry.
	DBTargetAuditTargetType = "db_target"
)

// DBTargetAuditEvent is one resource db target audit entry. It carries ids,
// host, database and outcome only: the password and connection string are
// never part of it (spec §11.2/§11.3).
type DBTargetAuditEvent struct {
	Action     string
	TargetType string
	TargetID   string
	ActorID    int64
	Metadata   map[string]any
}

// DBTargetAuditSink records one db target audit entry. A record failure is
// logged by the caller and never fails the business request (spec §11.2).
type DBTargetAuditSink interface {
	RecordDBTargetAudit(ctx context.Context, event DBTargetAuditEvent) error
}
