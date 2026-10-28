package application

import (
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// assertQueryReason asserts err is a *domain.QueryError with reason.
func assertQueryReason(t *testing.T, err error, reason string) {
	t.Helper()
	var queryErr *domain.QueryError
	if !errors.As(err, &queryErr) {
		t.Fatalf("err=%v (%T), want *domain.QueryError", err, err)
	}
	if queryErr.Reason != reason {
		t.Fatalf("reason=%q want %q (message=%q)", queryErr.Reason, reason, queryErr.Message)
	}
}

// TestValidateReadOnlySQLAccepts proves the four whitelisted statement kinds are
// accepted, including leading comments, a trailing semicolon, quoted
// identifiers/strings and dollar-quoted bodies.
func TestValidateReadOnlySQLAccepts(t *testing.T) {
	sqls := []string{
		"SELECT 1",
		"  select current_user",
		"-- leading comment\n/* block */ SELECT 1;",
		"WITH recent AS (SELECT id FROM orders) SELECT * FROM recent",
		"SHOW transaction_read_only",
		"EXPLAIN SELECT 1",
		"SELECT 'delete from t; -- not a statement'",
		"SELECT \"update\" FROM t",
		"SELECT $$delete from t;$$",
		"SELECT $tag$; update x$tag$",
		"SELECT 1 /* nested /* comment */ still comment */;",
		"select E'it\\'s ok'",
	}
	for _, sql := range sqls {
		if err := ValidateReadOnlySQL(sql); err != nil {
			t.Errorf("ValidateReadOnlySQL(%q) = %v, want nil", sql, err)
		}
	}
}

// TestValidateReadOnlySQLRejectsWrites proves the first-keyword whitelist and
// the forbidden-keyword list reject every write/DDL/admin statement.
func TestValidateReadOnlySQLRejectsWrites(t *testing.T) {
	sqls := []string{
		"UPDATE orders SET status = 'done'",
		"delete from orders",
		"INSERT INTO orders (id) VALUES (1)",
		"CREATE TABLE t (id int)",
		"ALTER TABLE t ADD COLUMN x int",
		"DROP TABLE t",
		"TRUNCATE t",
		"GRANT SELECT ON t TO public",
		"REVOKE SELECT ON t FROM public",
		"VACUUM t",
		"ANALYZE t",
		"COPY t TO STDOUT",
		"CALL proc()",
		"DO $$ BEGIN NULL; END $$",
		"SET statement_timeout = 0",
		"RESET ALL",
		"BEGIN",
		"COMMIT",
		"ROLLBACK",
	}
	for _, sql := range sqls {
		err := ValidateReadOnlySQL(sql)
		if err == nil {
			t.Errorf("ValidateReadOnlySQL(%q) accepted a write statement", sql)
			continue
		}
		assertQueryReason(t, err, domain.ReasonDBReadonlyViolation)
	}
}

// TestValidateReadOnlySQLRejectsWriteNestedInRead proves a data-modifying CTE is
// rejected even though the statement starts with WITH.
func TestValidateReadOnlySQLRejectsWriteNestedInRead(t *testing.T) {
	sql := "WITH gone AS (DELETE FROM orders RETURNING id) SELECT * FROM gone"
	assertQueryReason(t, ValidateReadOnlySQL(sql), domain.ReasonDBReadonlyViolation)
}

// TestValidateReadOnlySQLRejectsMultipleStatements proves a semicolon followed
// by more SQL is a read-only violation, while trailing/empty statements are
// accepted (covered by the accept table).
func TestValidateReadOnlySQLRejectsMultipleStatements(t *testing.T) {
	sqls := []string{
		"SELECT 1; DELETE FROM orders",
		"SELECT 1;\nSELECT 2",
		"/* c */ SELECT 1; ; SELECT 2",
	}
	for _, sql := range sqls {
		assertQueryReason(t, ValidateReadOnlySQL(sql), domain.ReasonDBReadonlyViolation)
	}
}

// TestValidateReadOnlySQLRejectsOversized proves the 16 KiB statement cap.
func TestValidateReadOnlySQLRejectsOversized(t *testing.T) {
	sql := "SELECT '" + strings.Repeat("a", MaxReadOnlySQLBytes) + "'"
	assertQueryReason(t, ValidateReadOnlySQL(sql), domain.ReasonDBReadonlyViolation)
}

// TestValidateReadOnlySQLRejectsMalformed proves unterminated literals and an
// empty statement are query failures rather than policy violations.
func TestValidateReadOnlySQLRejectsMalformed(t *testing.T) {
	for _, sql := range []string{"", "   ", "-- only a comment", "SELECT 'unterminated", "SELECT /* unterminated"} {
		assertQueryReason(t, ValidateReadOnlySQL(sql), domain.ReasonDBQueryFailed)
	}
}

// TestQueryLimitsForTarget proves the limit fallbacks: request beats target,
// target beats platform default, oversized request caps at the DDL/§5.7 bound.
func TestQueryLimitsForTarget(t *testing.T) {
	target := domain.DBTarget{MaxRows: 250, StatementTimeoutMS: 8000}
	limits := QueryLimitsForTarget(target, domain.QueryLimits{})
	if limits.MaxRows != 250 || limits.TimeoutMS != 8000 || limits.MaxBytes != MaxQueryResultBytes {
		t.Fatalf("limits=%+v want target rows/timeout and the byte cap", limits)
	}

	limits = QueryLimitsForTarget(domain.DBTarget{}, domain.QueryLimits{MaxRows: 10, TimeoutMS: 900})
	if limits.MaxRows != 10 || limits.TimeoutMS != 900 {
		t.Fatalf("limits=%+v want the requested values", limits)
	}

	limits = QueryLimitsForTarget(domain.DBTarget{}, domain.QueryLimits{})
	if limits.MaxRows != domain.DBTargetDefaultMaxRows ||
		limits.TimeoutMS != domain.DBTargetDefaultStatementTimeoutMS {
		t.Fatalf("limits=%+v want the platform defaults", limits)
	}

	limits = QueryLimitsForTarget(domain.DBTarget{MaxRows: 5000}, domain.QueryLimits{MaxRows: 5000, MaxBytes: 8 << 20})
	if limits.MaxRows != domain.DBTargetMaxRowsMax || limits.MaxBytes != MaxQueryResultBytes {
		t.Fatalf("limits=%+v want clamped rows/bytes", limits)
	}
}
