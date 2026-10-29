package resource

import (
	"context"
	"errors"
	"strings"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// fakeDBTargets is the resource db target read the adapter consumes.
type fakeDBTargets struct {
	target *resourcedomain.DBTarget
	err    error
	calls  int
	envID  int64
}

func (f *fakeDBTargets) Get(_ context.Context, environmentID int64) (*resourcedomain.DBTarget, error) {
	f.calls++
	f.envID = environmentID
	if f.err != nil {
		return nil, f.err
	}
	return f.target, nil
}

// fakeReadOnlyQuerier records the query it received and returns one scripted
// outcome.
type fakeReadOnlyQuerier struct {
	result    resourcedomain.QueryResult
	err       error
	calls     int
	target    resourcedomain.DBTarget
	sql       string
	gotLimits resourcedomain.QueryLimits
}

func (f *fakeReadOnlyQuerier) Query(_ context.Context, target resourcedomain.DBTarget, sql string, limits resourcedomain.QueryLimits) (resourcedomain.QueryResult, error) {
	f.calls++
	f.target = target
	f.sql = sql
	f.gotLimits = limits
	return f.result, f.err
}

// dbTargetFixture is one configured target of environment 9.
func dbTargetFixture() *resourcedomain.DBTarget {
	return &resourcedomain.DBTarget{
		ID: 41, EnvironmentID: 9, Name: "orders", Host: "127.0.0.1", Port: 5432,
		DatabaseName: "app", Username: "reader", Password: "plain-secret",
		SSLMode:          resourcedomain.DBTargetSSLModeDisable,
		ConnectTimeoutMS: 3000, StatementTimeoutMS: 2000, MaxRows: 50, Enabled: true,
	}
}

// TestDBQueryAdapterMapsTargetAndResult pins the execution projection: the
// adapter exposes the enabled target metadata, resolves the effective limits
// through the resource service, and maps the normalized result without leaking
// credentials.
func TestDBQueryAdapterMapsTargetAndResult(t *testing.T) {
	targets := &fakeDBTargets{target: dbTargetFixture()}
	querier := &fakeReadOnlyQuerier{result: resourcedomain.QueryResult{
		Columns: []string{"count"}, Rows: [][]string{{"3"}}, RowCount: 1, ElapsedMS: 5,
	}}
	adapter := NewDBQuery(targets, querier)

	ref, err := adapter.Target(context.Background(), 9)
	if err != nil {
		t.Fatalf("Target: %v", err)
	}
	if ref.EnvironmentID != 9 || ref.Name != "orders" || ref.MaxRows != 50 ||
		ref.StatementTimeoutMS != 2000 {
		t.Fatalf("target ref = %+v", ref)
	}

	result, err := adapter.Query(context.Background(), 9, "SELECT count(*) FROM orders",
		executionapp.DBQueryLimits{MaxRows: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if querier.calls != 1 || querier.sql != "SELECT count(*) FROM orders" {
		t.Fatalf("querier call = %d/%q", querier.calls, querier.sql)
	}
	if querier.target.Password != "plain-secret" {
		t.Fatal("the adapter must pass the stored target (including the password) to the querier")
	}
	// The request cap wins; the rest falls back to the target configuration.
	if querier.gotLimits.MaxRows != 10 || querier.gotLimits.TimeoutMS != 2000 ||
		querier.gotLimits.MaxBytes != resourceapp.MaxQueryResultBytes {
		t.Fatalf("resolved limits = %+v", querier.gotLimits)
	}
	if result.RowCount != 1 || result.ElapsedMS != 5 || len(result.Rows) != 1 ||
		result.Rows[0][0] != "3" || len(result.Columns) != 1 {
		t.Fatalf("result projection = %+v", result)
	}
}

// TestDBQueryAdapterTargetMissing pins spec §5.7/§7.11: a missing or disabled
// target is db_target_missing (environment), not a query failure.
func TestDBQueryAdapterTargetMissing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target *resourcedomain.DBTarget
		err    error
	}{
		{name: "unconfigured"},
		{name: "disabled", target: func() *resourcedomain.DBTarget {
			target := dbTargetFixture()
			target.Enabled = false
			return target
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := NewDBQuery(&fakeDBTargets{target: tc.target, err: tc.err}, &fakeReadOnlyQuerier{})
			_, err := adapter.Target(context.Background(), 9)
			requireDBQueryReason(t, err, executionapp.DBReasonTargetMissing)
			_, err = adapter.Query(context.Background(), 9, "SELECT 1", executionapp.DBQueryLimits{})
			requireDBQueryReason(t, err, executionapp.DBReasonTargetMissing)
		})
	}
}

// TestDBQueryAdapterMapsQueryErrors pins the resource QueryError reason mapping
// and the transport fallback: it never echoes the password.
func TestDBQueryAdapterMapsQueryErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{
			name:   "read-only violation",
			err:    resourcedomain.NewQueryError(resourcedomain.ReasonDBReadonlyViolation, "write keyword"),
			reason: executionapp.DBReasonReadonlyViolation,
		},
		{
			name:   "query failed",
			err:    resourcedomain.NewQueryError(resourcedomain.ReasonDBQueryFailed, "syntax error"),
			reason: executionapp.DBReasonQueryFailed,
		},
		{
			name:   "unknown resource reason falls back to query failure",
			err:    resourcedomain.NewQueryError("something_else", "boom"),
			reason: executionapp.DBReasonQueryFailed,
		},
		{
			name:   "transport failure is a connection failure",
			err:    errors.New("tls: handshake failed password=plain-secret"),
			reason: executionapp.DBReasonConnectionFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewDBQuery(&fakeDBTargets{target: dbTargetFixture()},
				&fakeReadOnlyQuerier{err: tt.err})
			_, err := adapter.Query(context.Background(), 9, "SELECT 1", executionapp.DBQueryLimits{})
			requireDBQueryReason(t, err, tt.reason)
			if strings.Contains(err.Error(), "plain-secret") {
				t.Fatalf("db query error echoed the password: %v", err)
			}
		})
	}
}

// TestDBQueryAdapterRejectsMisconfiguredDependencies pins the fail-closed
// wiring: a missing target service or querier is reported, never a panic.
func TestDBQueryAdapterRejectsMisconfiguredDependencies(t *testing.T) {
	if _, err := NewDBQuery(nil, &fakeReadOnlyQuerier{}).Target(context.Background(), 9); err == nil {
		t.Fatal("Target without a target service returned nil")
	}
	adapter := NewDBQuery(&fakeDBTargets{target: dbTargetFixture()}, nil)
	if _, err := adapter.Query(context.Background(), 9, "SELECT 1", executionapp.DBQueryLimits{}); err == nil {
		t.Fatal("Query without a querier returned nil")
	}
}

// requireDBQueryReason asserts err is a *DBQueryError with the desired reason.
func requireDBQueryReason(t *testing.T, err error, reason string) {
	t.Helper()
	var queryErr *executionapp.DBQueryError
	if !errors.As(err, &queryErr) {
		t.Fatalf("error = %v (%T), want *DBQueryError", err, err)
	}
	if queryErr.Reason != reason {
		t.Fatalf("reason = %q, want %q", queryErr.Reason, reason)
	}
}

// keep the interface assertions explicit: the production resource services
// must keep satisfying the adapter contracts.
var (
	_ interface {
		Get(context.Context, int64) (*resourcedomain.DBTarget, error)
	} = (*fakeDBTargets)(nil)
	_ resourceapp.ReadOnlyQuerier = (*fakeReadOnlyQuerier)(nil)
)
