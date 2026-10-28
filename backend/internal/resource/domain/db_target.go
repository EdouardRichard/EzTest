package domain

import (
	"fmt"
	"strings"
	"time"
)

// DB target bounds and defaults, mirrored from the db_target CHECK constraints
// (spec §4.2.4/§5.7).
const (
	// DBTargetNameMaxLength bounds db_target.name.
	DBTargetNameMaxLength = 100

	// DBTargetConnectTimeoutMinMS and DBTargetConnectTimeoutMaxMS bound
	// db_target.connect_timeout_ms.
	DBTargetConnectTimeoutMinMS = 500
	DBTargetConnectTimeoutMaxMS = 30000
	// DBTargetDefaultConnectTimeoutMS is the connect timeout applied when a
	// request omits one.
	DBTargetDefaultConnectTimeoutMS = 3000

	// DBTargetStatementTimeoutMinMS and DBTargetStatementTimeoutMaxMS bound
	// db_target.statement_timeout_ms.
	DBTargetStatementTimeoutMinMS = 500
	DBTargetStatementTimeoutMaxMS = 60000
	// DBTargetDefaultStatementTimeoutMS is the statement timeout applied when a
	// request omits one.
	DBTargetDefaultStatementTimeoutMS = 5000

	// DBTargetMaxRowsMin and DBTargetMaxRowsMax bound db_target.max_rows.
	DBTargetMaxRowsMin = 1
	DBTargetMaxRowsMax = 1000
	// DBTargetDefaultMaxRows is the row cap applied when a request omits one.
	DBTargetDefaultMaxRows = 100
)

// Supported db_target.ssl_mode values (the DDL CHECK constraint).
const (
	DBTargetSSLModeDisable    = "disable"
	DBTargetSSLModeRequire    = "require"
	DBTargetSSLModeVerifyCA   = "verify-ca"
	DBTargetSSLModeVerifyFull = "verify-full"
)

// db_target.last_test_status values. An empty status means "never tested".
const (
	DBTargetTestStatusNone   = ""
	DBTargetTestStatusOK     = "ok"
	DBTargetTestStatusFailed = "failed"
)

// Stable machine-readable reasons of the P7 db target and db assertion flow
// (spec §7.11). They are duplicated here instead of the error kinds so both the
// HTTP handlers and the execution db_assert tool share one vocabulary.
const (
	ReasonDBTargetInvalid     = "db_target_invalid"
	ReasonDBTargetMissing     = "db_target_missing"
	ReasonDBConnectionFailed  = "db_connection_failed"
	ReasonDBQueryFailed       = "db_query_failed"
	ReasonDBReadonlyViolation = "db_readonly_violation"
	ReasonDBResultTruncated   = "db_result_truncated"
	ReasonDBExpectedInvalid   = "db_expected_invalid"
)

// DBTarget is the single read-only PostgreSQL target of one environment
// (spec §5.7). Password is stored in plaintext by the D48 decision; it is used
// by the resource repositories and the read-only querier only and must never be
// copied into a DTO, event, audit entry, evidence file, log line or LLM prompt.
type DBTarget struct {
	ID                 int64
	EnvironmentID      int64
	Name               string
	Host               string
	Port               int
	DatabaseName       string
	Username           string
	Password           string
	SSLMode            string
	ConnectTimeoutMS   int
	StatementTimeoutMS int
	MaxRows            int
	Enabled            bool
	LastTestedAt       *time.Time
	LastTestStatus     string
	LastTestMessage    string
	CreatedBy          int64
	UpdatedBy          int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// DBTargetTestError is the classified failure of one connection test.
type DBTargetTestError struct {
	Reason  string
	Message string
}

// DBTargetTestResult is the outcome of a connection test (spec §7.8). A failed
// test is reported through Error with OK=false; it is not a transport error.
// The result never carries the password or a full DSN.
type DBTargetTestResult struct {
	OK                  bool
	LatencyMS           int64
	ServerVersion       string
	CurrentUser         string
	TransactionReadOnly bool
	Warnings            []string
	Error               *DBTargetTestError
}

// QueryResult is one normalized read-only query result (spec §5.7). Every cell
// is rendered as a string: SQL NULL becomes "null", booleans become "true" or
// "false", numbers use plain decimal notation without trailing fractional
// zeros, timestamps use RFC3339 and everything else its text form.
type QueryResult struct {
	Columns   []string
	Rows      [][]string
	RowCount  int
	Truncated bool
	ElapsedMS int64
}

// QueryLimits bounds one read-only query. Zero fields fall back to the target's
// configured values and then to the platform defaults.
type QueryLimits struct {
	MaxRows   int
	TimeoutMS int
	MaxBytes  int
}

// QueryError is a classified read-only query failure. Reason is one of the
// stable db_* reasons of spec §7.11 so the execution db_assert tool can map the
// failure onto environment/case outcomes without parsing messages.
type QueryError struct {
	Reason  string
	Message string
}

// Error implements error.
func (e *QueryError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Reason
}

// NewQueryError returns a classified query error.
func NewQueryError(reason, message string) *QueryError {
	return &QueryError{Reason: reason, Message: message}
}

// NewDBTargetMissing returns the 409 db_target_missing error for an environment
// whose read-only target is absent or disabled (spec §7.11).
func NewDBTargetMissing(environmentID int64) *Error {
	return &Error{
		Kind:    KindConflict,
		Reason:  ReasonDBTargetMissing,
		Message: fmt.Sprintf("environment %d has no enabled db target", environmentID),
	}
}

// dbTargetInvalid returns the 422 db_target_invalid error for field.
func dbTargetInvalid(field, message string) *Error {
	return NewInvalid(field, ReasonDBTargetInvalid, message)
}

// validateDBTargetSSLMode enforces the db_target.ssl_mode CHECK constraint.
func validateDBTargetSSLMode(mode string) error {
	switch mode {
	case DBTargetSSLModeDisable, DBTargetSSLModeRequire,
		DBTargetSSLModeVerifyCA, DBTargetSSLModeVerifyFull:
		return nil
	default:
		return dbTargetInvalid("sslmode", "sslmode must be disable, require, verify-ca or verify-full")
	}
}

// ValidateDBTarget enforces every db_target field bound of spec §4.2.4. The
// application always fills the optional fields with their DDL defaults before
// validating, so unset zero values are rejected here rather than reaching the
// database CHECK constraints.
func ValidateDBTarget(target *DBTarget) error {
	if target == nil {
		return dbTargetInvalid("dbTarget", "db target is required")
	}
	if target.EnvironmentID <= 0 {
		return dbTargetInvalid("environmentId", "environmentId must be a positive decimal id")
	}
	if len(target.Name) > DBTargetNameMaxLength {
		return dbTargetInvalid("name", "name must be at most 100 bytes")
	}
	if strings.TrimSpace(target.Host) == "" {
		return dbTargetInvalid("host", "host is required")
	}
	if target.Port < 1 || target.Port > 65535 {
		return dbTargetInvalid("port", "port must be between 1 and 65535")
	}
	if strings.TrimSpace(target.DatabaseName) == "" {
		return dbTargetInvalid("database", "database is required")
	}
	if strings.TrimSpace(target.Username) == "" {
		return dbTargetInvalid("username", "username is required")
	}
	if target.Password == "" {
		return dbTargetInvalid("password", "password is required the first time the target is configured")
	}
	if err := validateDBTargetSSLMode(target.SSLMode); err != nil {
		return err
	}
	if target.ConnectTimeoutMS < DBTargetConnectTimeoutMinMS || target.ConnectTimeoutMS > DBTargetConnectTimeoutMaxMS {
		return dbTargetInvalid("connectTimeoutMs", "connectTimeoutMs must be between 500 and 30000")
	}
	if target.StatementTimeoutMS < DBTargetStatementTimeoutMinMS || target.StatementTimeoutMS > DBTargetStatementTimeoutMaxMS {
		return dbTargetInvalid("statementTimeoutMs", "statementTimeoutMs must be between 500 and 60000")
	}
	if target.MaxRows < DBTargetMaxRowsMin || target.MaxRows > DBTargetMaxRowsMax {
		return dbTargetInvalid("maxRows", "maxRows must be between 1 and 1000")
	}
	return nil
}
