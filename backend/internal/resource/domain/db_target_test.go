package domain

import (
	"errors"
	"strings"
	"testing"
)

// validDBTarget returns a db target that passes every DDL-derived check.
func validDBTarget() *DBTarget {
	return &DBTarget{
		EnvironmentID:      42,
		Name:               "orders",
		Host:               "127.0.0.1",
		Port:               5432,
		DatabaseName:       "eztest_target",
		Username:           "eztest_ro",
		Password:           "s3cret",
		SSLMode:            DBTargetSSLModeDisable,
		ConnectTimeoutMS:   3000,
		StatementTimeoutMS: 5000,
		MaxRows:            100,
		Enabled:            true,
	}
}

// assertDBTargetInvalid asserts a db_target_invalid domain error for field.
func assertDBTargetInvalid(t *testing.T, err error, field string) {
	t.Helper()
	var domainErr *Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("err=%v (%T), want *domain.Error", err, err)
	}
	if domainErr.Kind != KindInvalid {
		t.Fatalf("kind=%q want invalid", domainErr.Kind)
	}
	if domainErr.Reason != ReasonDBTargetInvalid {
		t.Fatalf("reason=%q want %q", domainErr.Reason, ReasonDBTargetInvalid)
	}
	if domainErr.Field != field {
		t.Fatalf("field=%q want %q", domainErr.Field, field)
	}
}

// TestValidateDBTargetAcceptsValidTarget proves the happy path: a target that
// mirrors the db_target CHECK constraints validates cleanly.
func TestValidateDBTargetAcceptsValidTarget(t *testing.T) {
	if err := ValidateDBTarget(validDBTarget()); err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}
}

// TestValidateDBTargetRejectsUnsetOptionalFields proves the application must
// fill the DDL defaults before validating: an unset optional field is rejected
// rather than silently stored as zero.
func TestValidateDBTargetRejectsUnsetOptionalFields(t *testing.T) {
	cases := []struct {
		name   string
		field  string
		mutate func(*DBTarget)
	}{
		{"unset ssl mode", "sslmode", func(target *DBTarget) { target.SSLMode = "" }},
		{"unset connect timeout", "connectTimeoutMs", func(target *DBTarget) { target.ConnectTimeoutMS = 0 }},
		{"unset statement timeout", "statementTimeoutMs", func(target *DBTarget) { target.StatementTimeoutMS = 0 }},
		{"unset max rows", "maxRows", func(target *DBTarget) { target.MaxRows = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := validDBTarget()
			tc.mutate(target)
			assertDBTargetInvalid(t, ValidateDBTarget(target), tc.field)
		})
	}
}

// TestValidateDBTargetBounds covers every DDL-derived range and required field.
func TestValidateDBTargetBounds(t *testing.T) {
	longName := strings.Repeat("a", 101)
	cases := []struct {
		name   string
		field  string
		mutate func(*DBTarget)
	}{
		{"missing environment", "environmentId", func(target *DBTarget) { target.EnvironmentID = 0 }},
		{"name too long", "name", func(target *DBTarget) { target.Name = longName }},
		{"missing host", "host", func(target *DBTarget) { target.Host = "  " }},
		{"port low", "port", func(target *DBTarget) { target.Port = 0 }},
		{"port high", "port", func(target *DBTarget) { target.Port = 65536 }},
		{"missing database", "database", func(target *DBTarget) { target.DatabaseName = "" }},
		{"missing username", "username", func(target *DBTarget) { target.Username = "" }},
		{"missing password", "password", func(target *DBTarget) { target.Password = "" }},
		{"invalid ssl mode", "sslmode", func(target *DBTarget) { target.SSLMode = "allow" }},
		{"connect timeout low", "connectTimeoutMs", func(target *DBTarget) { target.ConnectTimeoutMS = 499 }},
		{"connect timeout high", "connectTimeoutMs", func(target *DBTarget) { target.ConnectTimeoutMS = 30001 }},
		{"statement timeout low", "statementTimeoutMs", func(target *DBTarget) { target.StatementTimeoutMS = 499 }},
		{"statement timeout high", "statementTimeoutMs", func(target *DBTarget) { target.StatementTimeoutMS = 60001 }},
		{"max rows low", "maxRows", func(target *DBTarget) { target.MaxRows = -1 }},
		{"max rows high", "maxRows", func(target *DBTarget) { target.MaxRows = 1001 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := validDBTarget()
			tc.mutate(target)
			assertDBTargetInvalid(t, ValidateDBTarget(target), tc.field)
		})
	}
}

// TestNewDBTargetMissing proves the missing/disabled target maps to the stable
// db_target_missing reason and a 409 conflict kind (spec §7.11).
func TestNewDBTargetMissing(t *testing.T) {
	err := NewDBTargetMissing(77)
	var domainErr *Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("err=%v (%T), want *domain.Error", err, err)
	}
	if domainErr.Kind != KindConflict || domainErr.Reason != ReasonDBTargetMissing {
		t.Fatalf("kind=%q reason=%q want conflict/%s", domainErr.Kind, domainErr.Reason, ReasonDBTargetMissing)
	}
	if !strings.Contains(domainErr.Message, "77") {
		t.Fatalf("message=%q should name the environment", domainErr.Message)
	}
}

// TestNewQueryError proves query failures carry the stable reason codes the
// execution db_assert tool maps onto step outcomes.
func TestNewQueryError(t *testing.T) {
	for _, reason := range []string{
		ReasonDBConnectionFailed, ReasonDBQueryFailed, ReasonDBReadonlyViolation,
		ReasonDBResultTruncated, ReasonDBExpectedInvalid,
	} {
		err := NewQueryError(reason, "boom")
		var queryErr *QueryError
		if !errors.As(err, &queryErr) {
			t.Fatalf("reason %q: err=%v (%T), want *QueryError", reason, err, err)
		}
		if queryErr.Reason != reason || queryErr.Message != "boom" {
			t.Fatalf("query error = %+v, want reason %q", queryErr, reason)
		}
		if !strings.Contains(err.Error(), "boom") {
			t.Fatalf("error string %q should carry the message", err.Error())
		}
	}
}
