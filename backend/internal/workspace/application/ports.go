// Package application holds the P8 todo-centre read model: the cross-context
// provider ports it consumes and the read-only service that aggregates them
// (design spec §6.3/D56).
//
// This file is the single home of the workspace cross-context port
// declarations. It contains value types and interfaces only: no pgx, HTTP or
// other-context imports. Adapters live in each context's
// infrastructure/workspace package and are wired by the composition root, so
// the workspace context never imports another bounded context.
package application

import (
	"context"
	"time"
)

// Todo kinds rendered by the todo centre (spec §6.3).
const (
	KindReview   = "review"
	KindTakeover = "takeover"
	KindGate     = "gate"
)

// Severity vocabulary of the todo items. The values are the rank hints the
// topbar panel uses for its badges; review is informational, a waiting takeover
// is urgent and an unresolved gate needs an administrator.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityMajor   = "major"
)

// DefaultLimit and MaxLimit bound the aggregated item list (env
// EZTEST_TODOS_MAX, spec §11.4).
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// ReasonInvalidArgument is the stable details.reason of the 400 envelope the
// HTTP layer renders for an out-of-range limit.
const ReasonInvalidArgument = "invalid_argument"

// ReviewRow is one version currently in the reviewing state (spec §6.3). The
// provider owns the version status filter; the service only aggregates.
type ReviewRow struct {
	VersionID   int64
	VersionName string
	Status      string
	// ReviewingAt is the timestamp surfaced as the item's createdAt. A version
	// row does not persist the exact transition time, so the provider uses the
	// last update, which is when the version most recently entered/refreshed
	// the reviewing state.
	ReviewingAt time.Time
}

// PendingRow is one execution case run that needs a human decision (spec
// §6.3). A non-nil TakeoverID means a waiting manual takeover exists for the
// run's session (`takeover`); a nil TakeoverID is an unresolved human gate with
// no waiting takeover (`gate`, the administrator-driven recovery case).
type PendingRow struct {
	CaseRunID     int64
	VersionID     int64
	VersionName   string
	CaseTitle     string
	FailureReason string
	CreatedAt     time.Time
	TakeoverID    *int64
}

// ReviewProvider supplies the versions currently waiting for review. The
// version context adapts its own tables to this port.
type ReviewProvider interface {
	ReviewingVersions(ctx context.Context) ([]ReviewRow, error)
}

// ExecutionProvider supplies the execution case runs waiting for a human
// takeover or gate escalation. The execution context adapts its own tables to
// this port.
type ExecutionProvider interface {
	PendingTakeovers(ctx context.Context) ([]PendingRow, error)
}

// Providers groups the todo-centre providers. Review is required by the service;
// Execution is optional and only queried for administrators.
type Providers struct {
	Review    ReviewProvider
	Execution ExecutionProvider
}
