package pgreadonly

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// fakeRows implements pgx.Rows for the result-collection tests.
type fakeRows struct {
	fields []pgconn.FieldDescription
	rows   [][]any
	index  int
}

// Close marks the rows closed.
func (r *fakeRows) Close() {}

// Err returns no stream error.
func (r *fakeRows) Err() error { return nil }

// CommandTag returns the zero command tag.
func (r *fakeRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }

// FieldDescriptions returns the configured columns.
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return r.fields }

// Next advances to the next row.
func (r *fakeRows) Next() bool {
	if r.index >= len(r.rows) {
		return false
	}
	r.index++
	return true
}

// Values returns the current row.
func (r *fakeRows) Values() ([]any, error) {
	if r.index == 0 || r.index > len(r.rows) {
		return nil, errors.New("no current row")
	}
	return r.rows[r.index-1], nil
}

// Scan is not used by collectRows.
func (r *fakeRows) Scan(...any) error { return errors.New("not implemented") }

// RawValues is not used by collectRows.
func (r *fakeRows) RawValues() [][]byte { return nil }

// Conn is not used by collectRows.
func (r *fakeRows) Conn() *pgx.Conn { return nil }

// columnsOf builds field descriptions for the named columns.
func columnsOf(names ...string) []pgconn.FieldDescription {
	fields := make([]pgconn.FieldDescription, len(names))
	for i, name := range names {
		fields[i] = pgconn.FieldDescription{Name: name}
	}
	return fields
}

// TestNormalizeQueryValue proves the §6.6.1 value normalization: NULL, booleans,
// decimal numbers without trailing zeros, RFC3339 timestamps and text.
func TestNormalizeQueryValue(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"null", nil, "null"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"int16", int16(-3), "-3"},
		{"int32", int32(42), "42"},
		{"int64", int64(9007199254740993), "9007199254740993"},
		{"int", 7, "7"},
		{"float trailing zeros", 1.5, "1.5"},
		{"float integral", 2.0, "2"},
		{"numeric trailing zeros", pgtype.Numeric{Int: big.NewInt(1500), Exp: -3, Valid: true}, "1.5"},
		{"numeric integral", pgtype.Numeric{Int: big.NewInt(100), Exp: -2, Valid: true}, "1"},
		{"numeric null", pgtype.Numeric{}, "null"},
		{"time", time.Date(2026, 10, 28, 9, 30, 0, 0, time.UTC), "2026-10-28T09:30:00Z"},
		{"time fraction", time.Date(2026, 10, 28, 9, 30, 0, 123000000, time.UTC), "2026-10-28T09:30:00.123Z"},
		{"bytes", []byte("raw"), "raw"},
		{"string", "text", "text"},
		{"uuid", [16]byte{0x0f, 0x8f, 0x1a, 0x2b, 0x3c, 0x4d, 0x5e, 0x6f, 0x70, 0x81, 0x92, 0xa3, 0xb4, 0xc5, 0xd6, 0xe7},
			"0f8f1a2b-3c4d-5e6f-7081-92a3b4c5d6e7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeQueryValue(tc.value); got != tc.want {
				t.Fatalf("normalizeQueryValue(%v) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestCollectRows proves the row cap uses maxRows and reports truncation.
func TestCollectRows(t *testing.T) {
	rows := &fakeRows{
		fields: columnsOf("id", "name"),
		rows: [][]any{
			{int64(1), "a"},
			{int64(2), "b"},
			{int64(3), "c"},
		},
	}
	result, err := collectRows(rows, domain.QueryLimits{MaxRows: 2, MaxBytes: 1024})
	if err != nil {
		t.Fatalf("collectRows: %v", err)
	}
	if !reflect.DeepEqual(result.Columns, []string{"id", "name"}) {
		t.Fatalf("columns=%v", result.Columns)
	}
	if result.RowCount != 2 || len(result.Rows) != 2 || !result.Truncated {
		t.Fatalf("result=%+v want 2 rows and truncated", result)
	}
	if result.Rows[0][0] != "1" || result.Rows[0][1] != "a" {
		t.Fatalf("rows=%v want normalized strings", result.Rows)
	}
}

// TestCollectRowsExactLimitNotTruncated proves a result with exactly maxRows
// rows is not truncated.
func TestCollectRowsExactLimitNotTruncated(t *testing.T) {
	rows := &fakeRows{
		fields: columnsOf("id"),
		rows:   [][]any{{int64(1)}, {int64(2)}},
	}
	result, err := collectRows(rows, domain.QueryLimits{MaxRows: 2, MaxBytes: 1024})
	if err != nil {
		t.Fatalf("collectRows: %v", err)
	}
	if result.RowCount != 2 || result.Truncated {
		t.Fatalf("result=%+v want exactly the limit and no truncation", result)
	}
}

// TestCollectRowsByteCap proves the total normalized byte cap reports
// truncation and never exceeds the cap.
func TestCollectRowsByteCap(t *testing.T) {
	rows := &fakeRows{
		fields: columnsOf("value"),
		rows: [][]any{
			{"abcdef"},
			{"ghijkl"},
		},
	}
	result, err := collectRows(rows, domain.QueryLimits{MaxRows: 100, MaxBytes: 10})
	if err != nil {
		t.Fatalf("collectRows: %v", err)
	}
	if !result.Truncated || result.RowCount != 1 || result.Rows[0][0] != "abcdef" {
		t.Fatalf("result=%+v want the first row and truncation", result)
	}
}

// fakeConnector returns a fixed error, so no network is touched.
type fakeConnector struct {
	err   error
	calls int
}

func (c *fakeConnector) connect(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) {
	c.calls++
	return nil, c.err
}

// validTarget returns a valid, enabled target for the querier tests.
func validTarget() domain.DBTarget {
	return domain.DBTarget{
		ID: 1, EnvironmentID: 7, Host: "127.0.0.1", Port: 5432,
		DatabaseName: "eztest_target", Username: "eztest_ro", Password: "s3cret",
		SSLMode: domain.DBTargetSSLModeDisable, ConnectTimeoutMS: 500,
		StatementTimeoutMS: 1000, MaxRows: 100, Enabled: true,
	}
}

// TestQueryRejectsInvalidSQLBeforeConnecting proves the static whitelist runs
// before any connection attempt.
func TestQueryRejectsInvalidSQLBeforeConnecting(t *testing.T) {
	connector := &fakeConnector{err: errors.New("must not connect")}
	querier := &Querier{connect: connector.connect}
	_, err := querier.Query(context.Background(), validTarget(), "DELETE FROM orders", domain.QueryLimits{})
	var queryErr *domain.QueryError
	if !errors.As(err, &queryErr) || queryErr.Reason != domain.ReasonDBReadonlyViolation {
		t.Fatalf("err=%v want db_readonly_violation", err)
	}
	if connector.calls != 0 {
		t.Fatal("the querier connected before rejecting a write statement")
	}
}

// TestQueryRejectsDisabledTarget proves a disabled target maps to
// db_target_missing before connecting (spec §5.7).
func TestQueryRejectsDisabledTarget(t *testing.T) {
	connector := &fakeConnector{err: errors.New("must not connect")}
	querier := &Querier{connect: connector.connect}
	target := validTarget()
	target.Enabled = false
	_, err := querier.Query(context.Background(), target, "SELECT 1", domain.QueryLimits{})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Reason != domain.ReasonDBTargetMissing {
		t.Fatalf("err=%v want db_target_missing", err)
	}
	if connector.calls != 0 {
		t.Fatal("the querier connected to a disabled target")
	}
}

// TestQueryMapsConnectFailure proves a connection failure maps to
// db_connection_failed without leaking the password or connection string.
func TestQueryMapsConnectFailure(t *testing.T) {
	connector := &fakeConnector{err: errors.New(`failed to connect: password=s3cret host=127.0.0.1`)}
	querier := &Querier{connect: connector.connect}
	_, err := querier.Query(context.Background(), validTarget(), "SELECT 1", domain.QueryLimits{})
	var queryErr *domain.QueryError
	if !errors.As(err, &queryErr) || queryErr.Reason != domain.ReasonDBConnectionFailed {
		t.Fatalf("err=%v want db_connection_failed", err)
	}
	if queryErr.Message == "" {
		t.Fatal("the failure message should be readable")
	}
	if containsSecret(queryErr.Message) {
		t.Fatalf("message=%q leaks the password", queryErr.Message)
	}
}

// TestTestConnectionMapsConnectFailure proves the probe reports a failed result
// (not a transport error) with the classified reason.
func TestTestConnectionMapsConnectFailure(t *testing.T) {
	connector := &fakeConnector{err: errors.New("dial tcp: connection refused")}
	querier := &Querier{connect: connector.connect}
	result, err := querier.TestConnection(context.Background(), validTarget())
	if err != nil {
		t.Fatalf("TestConnection returned a transport error: %v", err)
	}
	if result.OK || result.Error == nil || result.Error.Reason != domain.ReasonDBConnectionFailed {
		t.Fatalf("result=%+v want a db_connection_failed result", result)
	}
	if containsSecret(result.Error.Message) {
		t.Fatalf("message=%q leaks the password", result.Error.Message)
	}
}

// TestNewConnConfig proves the connection string carries the read-only default
// transaction mode and the per-target timeouts, and that the parsed config uses
// the target credentials.
func TestNewConnConfig(t *testing.T) {
	target := validTarget()
	cfg, err := newConnConfig(target)
	if err != nil {
		t.Fatalf("newConnConfig: %v", err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 5432 || cfg.Database != "eztest_target" ||
		cfg.User != "eztest_ro" || cfg.Password != "s3cret" {
		t.Fatalf("config=%+v want the target connection parameters", cfg)
	}
	if cfg.RuntimeParams["default_transaction_read_only"] != "on" {
		t.Fatalf("runtime params=%v want default_transaction_read_only=on", cfg.RuntimeParams)
	}
	if cfg.ConnectTimeout != 500*time.Millisecond {
		t.Fatalf("connect timeout=%v want 500ms", cfg.ConnectTimeout)
	}

	requireTLS := target
	requireTLS.SSLMode = domain.DBTargetSSLModeRequire
	cfg, err = newConnConfig(requireTLS)
	if err != nil {
		t.Fatalf("newConnConfig(require): %v", err)
	}
	if cfg.TLSConfig == nil {
		t.Fatal("sslmode=require must configure TLS")
	}
}

// TestClassifyQueryError proves the PostgreSQL error mapping of spec §5.7.
func TestClassifyQueryError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"read only transaction", &pgconn.PgError{Code: "25006"}, domain.ReasonDBReadonlyViolation},
		{"insufficient privilege", &pgconn.PgError{Code: "42501"}, domain.ReasonDBQueryFailed},
		{"syntax error", &pgconn.PgError{Code: "42601"}, domain.ReasonDBQueryFailed},
		{"statement timeout", &pgconn.PgError{Code: "57014"}, domain.ReasonDBConnectionFailed},
		{"context deadline", context.DeadlineExceeded, domain.ReasonDBConnectionFailed},
		{"context canceled", context.Canceled, domain.ReasonDBConnectionFailed},
		{"unknown pg error", &pgconn.PgError{Code: "XX000"}, domain.ReasonDBQueryFailed},
		{"plain error", errors.New("boom"), domain.ReasonDBQueryFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			queryErr := classifyQueryError(tc.err)
			if queryErr.Reason != tc.want {
				t.Fatalf("reason=%q want %q", queryErr.Reason, tc.want)
			}
			if queryErr.Message == "" {
				t.Fatal("message should be readable")
			}
			if containsSecret(queryErr.Message) {
				t.Fatalf("message=%q leaks the password", queryErr.Message)
			}
		})
	}
}

// containsSecret reports whether a message carries the test password or a DSN
// fragment.
func containsSecret(message string) bool {
	return strings.Contains(message, "s3cret") || strings.Contains(message, "password=")
}
