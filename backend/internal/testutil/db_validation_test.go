package testutil

import (
	"strings"
	"testing"
)

// TestValidateTestDatabaseURL pins the destructive-helper guard: integration
// tests drop and recreate the public schema, so the URL must name a dedicated
// PostgreSQL test/e2e database and must never resolve to the development
// database. The checks are pure, so this test needs no database.
func TestValidateTestDatabaseURL(t *testing.T) {
	const devURL = "postgres://dev:secret@localhost:5433/eztest?sslmode=disable"

	// pgx applies PG* environment variables when the URL leaves a setting out
	// (notably PGPORT), so the effective target would depend on the host
	// environment. The validator deliberately parses the same effective
	// configuration; keep the port deterministic here. An empty value is
	// ignored by pgx, restoring the 5432 default.
	t.Setenv("PGPORT", "")

	tests := []struct {
		name        string
		testURL     string
		devURL      string
		wantErr     bool
		errContains string
	}{
		{
			name:        "empty test URL",
			testURL:     "",
			devURL:      devURL,
			wantErr:     true,
			errContains: "not set",
		},
		{
			name:        "non-postgres scheme",
			testURL:     "mysql://user:pass@localhost:3306/eztest_test",
			devURL:      devURL,
			wantErr:     true,
			errContains: "postgres",
		},
		{
			name:        "malformed URL",
			testURL:     "postgres://user:pass@localhost:5433/%zz",
			devURL:      devURL,
			wantErr:     true,
			errContains: "valid URL",
		},
		{
			name:        "missing host",
			testURL:     "postgres:///eztest_test",
			devURL:      devURL,
			wantErr:     true,
			errContains: "host",
		},
		{
			name:        "missing database name",
			testURL:     "postgres://user:pass@localhost:5433/",
			devURL:      devURL,
			wantErr:     true,
			errContains: "database name",
		},
		{
			name:        "development database name",
			testURL:     "postgres://user:pass@localhost:5433/eztest",
			devURL:      devURL,
			wantErr:     true,
			errContains: "_test or _e2e",
		},
		{
			name:        "unsafe test suffix",
			testURL:     "postgres://user:pass@localhost:5433/eztest_test_backup",
			devURL:      devURL,
			wantErr:     true,
			errContains: "_test or _e2e",
		},
		{
			name:        "identical URLs",
			testURL:     "postgres://user:pass@localhost:5433/eztest_test",
			devURL:      "postgres://user:pass@localhost:5433/eztest_test",
			wantErr:     true,
			errContains: "same database",
		},
		{
			name:        "canonical match ignores credentials port form and query",
			testURL:     "postgres://other:pass@LOCALHOST/eztest_test?sslmode=require",
			devURL:      "postgres://dev:secret@localhost:5432/eztest_test",
			wantErr:     true,
			errContains: "same database",
		},
		{
			// pgx lets the dbname query parameter override the URL path. The
			// path looks safe but the connection would open the dev database.
			name:        "dbname query override reaches development database",
			testURL:     "postgres://user:pass@localhost:5433/eztest_test?dbname=eztest",
			devURL:      devURL,
			wantErr:     true,
			errContains: "_test or _e2e",
		},
		{
			// database is the canonical pgx name for the same setting.
			name:        "database query override reaches development database",
			testURL:     "postgres://user:pass@localhost:5433/eztest_test?database=eztest",
			devURL:      devURL,
			wantErr:     true,
			errContains: "_test or _e2e",
		},
		{
			// The URL text points at a remote host, but pgx connects to the
			// dev host:port because query parameters win over host:port.
			name:        "host query override reaches development database",
			testURL:     "postgres://user:pass@remote.example.com:6000/eztest_test?host=localhost&port=5433",
			devURL:      "postgres://dev:secret@localhost:5433/eztest_test",
			wantErr:     true,
			errContains: "same database",
		},
		{
			name:        "port query override reaches development database",
			testURL:     "postgres://user:pass@localhost:6000/eztest_test?port=5433",
			devURL:      "postgres://dev:secret@localhost:5433/eztest_test",
			wantErr:     true,
			errContains: "same database",
		},
		{
			name:    "safe test database with sslmode query",
			testURL: "postgres://user:pass@localhost:5433/eztest_test?sslmode=disable",
			devURL:  devURL,
			wantErr: false,
		},
		{
			name:    "safe test database",
			testURL: "postgres://user:pass@localhost:5433/eztest_test",
			devURL:  devURL,
			wantErr: false,
		},
		{
			name:    "safe postgresql scheme e2e database",
			testURL: "postgresql://user:pass@localhost:5433/eztest_e2e",
			devURL:  devURL,
			wantErr: false,
		},
		{
			name:    "same database name on another host is safe",
			testURL: "postgres://user:pass@test-db.example.com:5433/eztest_test",
			devURL:  devURL,
			wantErr: false,
		},
		{
			name:    "development URL absent skips the comparison",
			testURL: "postgres://user:pass@localhost:5433/eztest_test",
			devURL:  "",
			wantErr: false,
		},
		{
			name:        "malformed development URL fails closed",
			testURL:     "postgres://user:pass@localhost:5433/eztest_test",
			devURL:      "not a postgres URL",
			wantErr:     true,
			errContains: "EZTEST_DATABASE_URL",
		},
		{
			name:        "development URL without a host fails closed",
			testURL:     "postgres://user:pass@localhost:5433/eztest_test",
			devURL:      "postgres:///eztest",
			wantErr:     true,
			errContains: "EZTEST_DATABASE_URL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTestDatabaseURL(tc.testURL, tc.devURL)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateTestDatabaseURL(%q, %q) = nil, want error", tc.testURL, tc.devURL)
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("error %q does not contain %q", err, tc.errContains)
				}
				// Error messages must never leak credentials from the URL.
				if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "pass") {
					t.Fatalf("error leaks credentials: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateTestDatabaseURL(%q, %q) = %v, want nil", tc.testURL, tc.devURL, err)
			}
		})
	}
}
