// Package resource adapts the resource context's application services to the
// execution context's ports. This file adapts the environment read-only db
// target service and querier to application.DBQueryPort for the P7
// execution.db_assert tool (spec §5.7, §9.5).
package resource

import (
	"context"
	"errors"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// DBTargets is the narrow resource db target read the adapter needs.
// *resource/application.DBTargetService satisfies it.
type DBTargets interface {
	Get(ctx context.Context, environmentID int64) (*resourcedomain.DBTarget, error)
}

// DBQueryAdapter implements execution application.DBQueryPort over the resource
// db target service and read-only querier. The password never leaves this
// adapter/resource boundary: the projection, the results and every error are
// credential-free.
type DBQueryAdapter struct {
	targets DBTargets
	querier resourceapp.ReadOnlyQuerier
}

// NewDBQuery returns the db query adapter. Missing dependencies fail closed on
// the call that needs them.
func NewDBQuery(targets DBTargets, querier resourceapp.ReadOnlyQuerier) *DBQueryAdapter {
	return &DBQueryAdapter{targets: targets, querier: querier}
}

var _ executionapp.DBQueryPort = (*DBQueryAdapter)(nil)

// The production resource service must keep satisfying the adapter contract; a
// signature drift fails the build here rather than at the composition root.
var _ DBTargets = (*resourceapp.DBTargetService)(nil)

// Target returns the enabled read-only target projection of environmentID. A
// missing or disabled target is db_target_missing (spec §5.7/§7.11).
func (a *DBQueryAdapter) Target(ctx context.Context, environmentID int64) (executionapp.DBTargetRef, error) {
	target, err := a.loadEnabledTarget(ctx, environmentID)
	if err != nil {
		return executionapp.DBTargetRef{}, err
	}
	return executionapp.DBTargetRef{
		EnvironmentID:      target.EnvironmentID,
		Name:               target.Name,
		MaxRows:            target.MaxRows,
		StatementTimeoutMS: target.StatementTimeoutMS,
	}, nil
}

// Query executes one read-only statement against the environment target. The
// resource querier enforces the three read-only layers and the SQL whitelist;
// this adapter only resolves the target and the effective limits and maps the
// classified errors onto the execution vocabulary.
func (a *DBQueryAdapter) Query(ctx context.Context, environmentID int64, sql string, limits executionapp.DBQueryLimits) (executionapp.DBQueryResult, error) {
	target, err := a.loadEnabledTarget(ctx, environmentID)
	if err != nil {
		return executionapp.DBQueryResult{}, err
	}
	if a == nil || a.querier == nil {
		return executionapp.DBQueryResult{}, errors.New("execution: resource db querier is not configured")
	}
	resolved := resourceapp.QueryLimitsForTarget(*target, resourcedomain.QueryLimits{
		MaxRows:   limits.MaxRows,
		TimeoutMS: limits.TimeoutMS,
		MaxBytes:  limits.MaxBytes,
	})
	result, err := a.querier.Query(ctx, *target, sql, resolved)
	if err != nil {
		var queryErr *resourcedomain.QueryError
		if errors.As(err, &queryErr) {
			return executionapp.DBQueryResult{}, &executionapp.DBQueryError{
				Reason:  mapDBQueryReason(queryErr.Reason),
				Message: queryErr.Message,
			}
		}
		// The resource querier already classifies connect/auth/timeout as
		// db_connection_failed; any other transport failure is still an
		// environment failure. The underlying error is deliberately not echoed
		// because it may carry a DSN.
		return executionapp.DBQueryResult{}, &executionapp.DBQueryError{
			Reason:  executionapp.DBReasonConnectionFailed,
			Message: "the read-only database query failed",
		}
	}
	return executionapp.DBQueryResult{
		Columns:   append([]string(nil), result.Columns...),
		Rows:      copyDBRows(result.Rows),
		RowCount:  result.RowCount,
		Truncated: result.Truncated,
		ElapsedMS: result.ElapsedMS,
	}, nil
}

// loadEnabledTarget reads the environment target and reports a missing or
// disabled one as db_target_missing.
func (a *DBQueryAdapter) loadEnabledTarget(ctx context.Context, environmentID int64) (*resourcedomain.DBTarget, error) {
	if a == nil || a.targets == nil {
		return nil, errors.New("execution: resource db target service is not configured")
	}
	target, err := a.targets.Get(ctx, environmentID)
	if err != nil {
		var domainErr *resourcedomain.Error
		if errors.As(err, &domainErr) &&
			(domainErr.Kind == resourcedomain.KindNotFound ||
				domainErr.Reason == resourcedomain.ReasonDBTargetMissing) {
			return nil, dbTargetMissingError()
		}
		return nil, err
	}
	if target == nil || !target.Enabled {
		return nil, dbTargetMissingError()
	}
	return target, nil
}

// dbTargetMissingError builds the stable missing-target failure.
func dbTargetMissingError() error {
	return &executionapp.DBQueryError{
		Reason:  executionapp.DBReasonTargetMissing,
		Message: "the environment has no enabled read-only db target",
	}
}

// mapDBQueryReason passes the resource db vocabulary through and maps an
// unknown classified reason onto db_query_failed.
func mapDBQueryReason(reason string) string {
	switch reason {
	case resourcedomain.ReasonDBTargetMissing,
		resourcedomain.ReasonDBConnectionFailed,
		resourcedomain.ReasonDBQueryFailed,
		resourcedomain.ReasonDBReadonlyViolation,
		resourcedomain.ReasonDBResultTruncated,
		resourcedomain.ReasonDBExpectedInvalid:
		return reason
	default:
		return executionapp.DBReasonQueryFailed
	}
}

// copyDBRows returns an owned copy of the normalized rows.
func copyDBRows(rows [][]string) [][]string {
	if rows == nil {
		return nil
	}
	copied := make([][]string, len(rows))
	for i, row := range rows {
		copied[i] = append([]string(nil), row...)
	}
	return copied
}
