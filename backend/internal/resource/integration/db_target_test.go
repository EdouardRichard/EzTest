//go:build integration

package integration_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/pgreadonly"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// integrationDBTargetID and integrationDBTargetEnv are the deterministic ids of
// the repository test rows (the application mints them in production).
const (
	integrationDBTargetID  int64 = 880001
	integrationDBTargetEnv int64 = 440001
)

// integrationDBTarget returns a stored-target fixture.
func integrationDBTarget() domain.DBTarget {
	return domain.DBTarget{
		ID:                 integrationDBTargetID,
		EnvironmentID:      integrationDBTargetEnv,
		Name:               "orders",
		Host:               "127.0.0.1",
		Port:               5432,
		DatabaseName:       "eztest_target",
		Username:           "eztest_ro",
		Password:           "plaintext-secret",
		SSLMode:            domain.DBTargetSSLModeDisable,
		ConnectTimeoutMS:   3000,
		StatementTimeoutMS: 5000,
		MaxRows:            100,
		Enabled:            true,
		CreatedBy:          11,
		UpdatedBy:          11,
	}
}

// requireDomainReason asserts a *domain.Error with the given reason.
func requireDomainReason(t *testing.T, err error, reason string) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("err=%v (%T), want *domain.Error", err, err)
	}
	if domainErr.Reason != reason {
		t.Fatalf("reason=%q want %q", domainErr.Reason, reason)
	}
}

// TestDBTargetRepositoryRoundTrip proves the repository stores the plaintext
// password (D48), enforces one target per environment, records the last test
// outcome and implements the missing-target contract.
func TestDBTargetRepositoryRoundTrip(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewDBTargetRepository(pool)
	ctx := context.Background()

	if target, err := repo.GetDBTarget(ctx, integrationDBTargetEnv); err != nil || target != nil {
		t.Fatalf("Get missing = (%v, %v), want (nil, nil)", target, err)
	}
	if err := repo.DeleteDBTarget(ctx, integrationDBTargetEnv); err == nil {
		t.Fatal("deleting a missing target must report db_target_missing")
	} else {
		requireDomainReason(t, err, domain.ReasonDBTargetMissing)
	}

	target := integrationDBTarget()
	if err := repo.UpsertDBTarget(ctx, &target); err != nil {
		t.Fatalf("Upsert create: %v", err)
	}
	if target.ID != integrationDBTargetID || target.CreatedAt.IsZero() || target.UpdatedAt.IsZero() {
		t.Fatalf("target=%+v want the stored id and timestamps", target)
	}
	if target.LastTestStatus != domain.DBTargetTestStatusNone || target.LastTestedAt != nil {
		t.Fatalf("target=%+v want an untested row", target)
	}

	stored, err := repo.GetDBTarget(ctx, integrationDBTargetEnv)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored == nil {
		t.Fatal("Get returned no target after the upsert")
	}
	if stored.ID != target.ID || stored.Name != "orders" || stored.Host != "127.0.0.1" || stored.Port != 5432 ||
		stored.DatabaseName != "eztest_target" || stored.Username != "eztest_ro" ||
		stored.Password != "plaintext-secret" || stored.SSLMode != domain.DBTargetSSLModeDisable ||
		stored.ConnectTimeoutMS != 3000 || stored.StatementTimeoutMS != 5000 ||
		stored.MaxRows != 100 || !stored.Enabled {
		t.Fatalf("stored=%+v want the written target including the plaintext password", stored)
	}
	var rawPassword string
	if err := pool.QueryRow(ctx, `SELECT password FROM db_target WHERE environment_id=$1`, integrationDBTargetEnv).Scan(&rawPassword); err != nil {
		t.Fatalf("read raw password: %v", err)
	}
	if rawPassword != "plaintext-secret" {
		t.Fatalf("raw password=%q want plaintext (D48)", rawPassword)
	}

	// A second upsert of the same environment keeps the original id and created
	// metadata but replaces every mutable column.
	second := integrationDBTarget()
	second.ID = integrationDBTargetID + 1
	second.Name = "orders v2"
	second.Enabled = false
	second.UpdatedBy = 12
	second.SSLMode = domain.DBTargetSSLModeRequire
	if err := repo.UpsertDBTarget(ctx, &second); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	stored, err = repo.GetDBTarget(ctx, integrationDBTargetEnv)
	if err != nil || stored == nil {
		t.Fatalf("Get after update: (%v, %v)", stored, err)
	}
	if stored.ID != integrationDBTargetID {
		t.Fatalf("id=%d want the original id %d", stored.ID, integrationDBTargetID)
	}
	if stored.Name != "orders v2" || stored.Enabled || stored.SSLMode != domain.DBTargetSSLModeRequire ||
		stored.UpdatedBy != 12 || stored.CreatedBy != 11 {
		t.Fatalf("stored=%+v want the updated columns and the preserved created_by", stored)
	}

	testedAt := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	if err := repo.UpdateDBTargetTestResult(ctx, stored.ID, testedAt, domain.DBTargetTestStatusFailed, "connection refused"); err != nil {
		t.Fatalf("UpdateDBTargetTestResult: %v", err)
	}
	stored, err = repo.GetDBTarget(ctx, integrationDBTargetEnv)
	if err != nil || stored == nil {
		t.Fatalf("Get after test update: (%v, %v)", stored, err)
	}
	if stored.LastTestedAt == nil || !stored.LastTestedAt.Equal(testedAt) ||
		stored.LastTestStatus != domain.DBTargetTestStatusFailed || stored.LastTestMessage != "connection refused" {
		t.Fatalf("stored=%+v want the recorded test outcome", stored)
	}

	if err := repo.DeleteDBTarget(ctx, integrationDBTargetEnv); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if target, err := repo.GetDBTarget(ctx, integrationDBTargetEnv); err != nil || target != nil {
		t.Fatalf("Get after delete = (%v, %v), want (nil, nil)", target, err)
	}
}

// queryTargetFromEnv builds a domain target from the test database URL, so the
// read-only querier probes the same local PostgreSQL the repository test uses.
func queryTargetFromEnv(t *testing.T) domain.DBTarget {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("EZTEST_TEST_DATABASE_URL"))
	if raw == "" {
		t.Skip("EZTEST_TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	password, _ := parsed.User.Password()
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("test database URL has no valid port: %v", err)
	}
	sslMode := parsed.Query().Get("sslmode")
	switch sslMode {
	case domain.DBTargetSSLModeDisable, domain.DBTargetSSLModeRequire,
		domain.DBTargetSSLModeVerifyCA, domain.DBTargetSSLModeVerifyFull:
	default:
		sslMode = domain.DBTargetSSLModeDisable
	}
	return domain.DBTarget{
		EnvironmentID:      integrationDBTargetEnv + 1,
		Host:               parsed.Hostname(),
		Port:               port,
		DatabaseName:       strings.TrimPrefix(parsed.Path, "/"),
		Username:           parsed.User.Username(),
		Password:           password,
		SSLMode:            sslMode,
		ConnectTimeoutMS:   3000,
		StatementTimeoutMS: 5000,
		MaxRows:            100,
		Enabled:            true,
	}
}

// TestDBTargetReadOnlyQuerier exercises the focused real-PostgreSQL read-only
// path of spec §5.7/§12.2: metadata probe, value normalization, row truncation,
// statement timeout, write rejection and error mapping.
func TestDBTargetReadOnlyQuerier(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	target := queryTargetFromEnv(t)
	querier := pgreadonly.NewQuerier()
	ctx := context.Background()

	result, err := querier.TestConnection(ctx, target)
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if !result.OK || result.Error != nil {
		t.Fatalf("TestConnection result=%+v want ok", result)
	}
	if !strings.Contains(result.ServerVersion, "PostgreSQL") {
		t.Fatalf("serverVersion=%q", result.ServerVersion)
	}
	if result.CurrentUser != target.Username || !result.TransactionReadOnly {
		t.Fatalf("result=%+v want the read-only control probe", result)
	}
	if result.Warnings == nil {
		t.Fatal("warnings must be an empty slice, not nil")
	}
	// The local test database usually logs in as a superuser; the probe must
	// then warn that the role can write (spec §5.7).
	var superuser, createRole, createDB bool
	if err := pool.QueryRow(ctx,
		`SELECT rolsuper, rolcreaterole, rolcreatedb FROM pg_roles WHERE rolname = current_user`).Scan(
		&superuser, &createRole, &createDB); err != nil {
		t.Fatalf("read role privileges: %v", err)
	}
	hasWriteWarning := false
	for _, warning := range result.Warnings {
		if warning == pgreadonly.RoleWarningWritePrivileges {
			hasWriteWarning = true
		}
	}
	if (superuser || createRole || createDB) != hasWriteWarning {
		t.Fatalf("warnings=%v superuser=%v createRole=%v createDB=%v", result.Warnings, superuser, createRole, createDB)
	}

	query, err := querier.Query(ctx, target, `SELECT 1 AS n, 'x' AS s, NULL::text AS z, true AS b, 1.50::numeric AS num`, domain.QueryLimits{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if query.RowCount != 1 || query.Truncated {
		t.Fatalf("query=%+v want one untruncated row", query)
	}
	want := []string{"1", "x", "null", "true", "1.5"}
	for i, cell := range query.Rows[0] {
		if cell != want[i] {
			t.Fatalf("cell %d=%q want %q (row=%v)", i, cell, want[i], query.Rows[0])
		}
	}

	truncated, err := querier.Query(ctx, target, `SELECT generate_series(1, 5) AS n`,
		domain.QueryLimits{MaxRows: 2})
	if err != nil {
		t.Fatalf("Query truncated: %v", err)
	}
	if !truncated.Truncated || truncated.RowCount != 2 {
		t.Fatalf("query=%+v want 2 rows and truncated", truncated)
	}

	started := time.Now()
	if _, err := querier.Query(ctx, target, `SELECT pg_sleep(1)`,
		domain.QueryLimits{TimeoutMS: 500}); err == nil {
		t.Fatal("a statement over the timeout must fail")
	} else {
		// A statement timeout is an environment failure (T15 review): the
		// execution.db_assert tool must prescribe blocked(environment), not
		// inconclusive(case).
		requireQueryReason(t, err, domain.ReasonDBConnectionFailed)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("statement timeout took %v", elapsed)
	}

	if _, err := querier.Query(ctx, target, `UPDATE db_target SET name='x'`, domain.QueryLimits{}); err == nil {
		t.Fatal("a write statement must be rejected")
	} else {
		requireQueryReason(t, err, domain.ReasonDBReadonlyViolation)
	}
	if _, err := querier.Query(ctx, target, `SELECT * FROM definitely_missing_table`, domain.QueryLimits{}); err == nil {
		t.Fatal("a SQL error must be reported")
	} else {
		requireQueryReason(t, err, domain.ReasonDBQueryFailed)
	}

	unreachable := target
	unreachable.Port = 1
	unreachable.ConnectTimeoutMS = 500
	if _, err := querier.Query(ctx, unreachable, `SELECT 1`, domain.QueryLimits{}); err == nil {
		t.Fatal("an unreachable target must be reported")
	} else {
		requireQueryReason(t, err, domain.ReasonDBConnectionFailed)
	}
	disabled := target
	disabled.Enabled = false
	if _, err := querier.Query(ctx, disabled, `SELECT 1`, domain.QueryLimits{}); err == nil {
		t.Fatal("a disabled target must be reported")
	} else {
		requireDomainReason(t, err, domain.ReasonDBTargetMissing)
	}
}

// requireQueryReason asserts a *domain.QueryError with the given reason.
func requireQueryReason(t *testing.T, err error, reason string) {
	t.Helper()
	var queryErr *domain.QueryError
	if !errors.As(err, &queryErr) {
		t.Fatalf("err=%v (%T), want *domain.QueryError", err, err)
	}
	if queryErr.Reason != reason {
		t.Fatalf("reason=%q want %q (message=%q)", queryErr.Reason, reason, queryErr.Message)
	}
}
