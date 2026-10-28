// Package pgreadonly implements the resource context's read-only PostgreSQL
// query executor (spec §5.7). It opens one connection per call with
// default_transaction_read_only=on, runs the statement inside a
// BEGIN READ ONLY transaction with SET LOCAL statement_timeout, enforces the
// static SQL whitelist of the application layer, and caps the result by rows and
// bytes. The connection password is only used to dial the target and is never
// part of an error message, log line or result.
package pgreadonly

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// RoleWarningWritePrivileges is the warning emitted when the target login role
// can create databases, create roles or is a superuser (spec §5.7).
const RoleWarningWritePrivileges = "role_has_write_privileges"

// readOnlyControlSQL returns the session metadata probe of spec §5.7.
const readOnlyControlSQL = `SELECT version(), current_setting('transaction_read_only'), current_user`

// rolePrivilegesSQL reports the write-capable attributes of the login role.
const rolePrivilegesSQL = `SELECT rolsuper, rolcreaterole, rolcreatedb FROM pg_roles WHERE rolname = current_user`

// connectFunc dials one target connection. It is injectable so the failure
// mapping can be tested without a live database.
type connectFunc func(ctx context.Context, cfg *pgx.ConnConfig) (*pgx.Conn, error)

// Querier is the read-only query executor and connection prober of the resource
// context.
type Querier struct {
	connect connectFunc
}

// NewQuerier returns a querier that dials targets with pgx.
func NewQuerier() *Querier {
	return &Querier{connect: pgx.ConnectConfig}
}

var (
	_ application.ReadOnlyQuerier = (*Querier)(nil)
	_ application.DBTargetProber  = (*Querier)(nil)
)

// Query executes exactly one whitelisted read-only statement. The failure
// classification follows spec §5.7: connect/auth/DNS/timeout errors are
// db_connection_failed, SQL/permission/read-only errors are db_query_failed or
// db_readonly_violation. Result truncation is reported through
// QueryResult.Truncated rather than an error.
func (q *Querier) Query(ctx context.Context, target domain.DBTarget, sql string, limits domain.QueryLimits) (domain.QueryResult, error) {
	if err := application.ValidateReadOnlySQL(sql); err != nil {
		return domain.QueryResult{}, err
	}
	if !target.Enabled {
		return domain.QueryResult{}, domain.NewDBTargetMissing(target.EnvironmentID)
	}
	limits = application.QueryLimitsForTarget(target, limits)

	cfg, err := newConnConfig(target)
	if err != nil {
		return domain.QueryResult{}, domain.NewQueryError(domain.ReasonDBConnectionFailed,
			"the target connection parameters are invalid")
	}
	conn, err := q.dial(ctx, cfg, connectTimeout(target))
	if err != nil {
		return domain.QueryResult{}, connectionFailed()
	}
	defer closeConn(conn)

	started := time.Now()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.QueryResult{}, connectionFailed()
	}
	defer func() {
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", limits.TimeoutMS)); err != nil {
		return domain.QueryResult{}, queryFailed("the target rejected the statement timeout configuration")
	}
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return domain.QueryResult{}, classifyQueryError(err)
	}
	result, err := collectRows(rows, limits)
	rows.Close()
	if err != nil {
		return domain.QueryResult{}, err
	}
	result.ElapsedMS = time.Since(started).Milliseconds()
	return result, nil
}

// TestConnection probes the target with the read-only control query and the
// role privilege check. A probe failure is reported in the result with
// OK=false; the error return is reserved for internal failures.
func (q *Querier) TestConnection(ctx context.Context, target domain.DBTarget) (domain.DBTargetTestResult, error) {
	started := time.Now()
	cfg, err := newConnConfig(target)
	if err != nil {
		return failureResult(started, domain.ReasonDBConnectionFailed,
			"the target connection parameters are invalid"), nil
	}
	conn, err := q.dial(ctx, cfg, connectTimeout(target))
	if err != nil {
		return failureResult(started, domain.ReasonDBConnectionFailed,
			"failed to connect to the target database"), nil
	}
	defer closeConn(conn)

	var version, readOnly, currentUser string
	if err := conn.QueryRow(ctx, readOnlyControlSQL).Scan(&version, &readOnly, &currentUser); err != nil {
		classified := classifyQueryError(err)
		return failureResult(started, classified.Reason, classified.Message), nil
	}
	result := domain.DBTargetTestResult{
		OK:                  true,
		ServerVersion:       version,
		CurrentUser:         currentUser,
		TransactionReadOnly: strings.EqualFold(readOnly, "on"),
		Warnings:            []string{},
	}
	var superuser, createRole, createDB bool
	if err := conn.QueryRow(ctx, rolePrivilegesSQL).Scan(&superuser, &createRole, &createDB); err == nil {
		if superuser || createRole || createDB {
			result.Warnings = append(result.Warnings, RoleWarningWritePrivileges)
		}
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	return result, nil
}

// dial connects with the target's connect timeout and wraps the pgx dialer.
// The dial error is deliberately not returned to the caller (it may echo the
// connection string); only the classified reason crosses the boundary.
func (q *Querier) dial(ctx context.Context, cfg *pgx.ConnConfig, timeout time.Duration) (*pgx.Conn, error) {
	connect := q.connect
	if connect == nil {
		connect = pgx.ConnectConfig
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return connect(dialCtx, cfg)
}

// closeConn closes the connection with a background context so a canceled
// request context still releases the server session.
func closeConn(conn *pgx.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Close(context.WithoutCancel(context.Background()))
}

// connectTimeout resolves the target connect timeout, falling back to the DDL
// default.
func connectTimeout(target domain.DBTarget) time.Duration {
	timeoutMS := target.ConnectTimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = domain.DBTargetDefaultConnectTimeoutMS
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

// newConnConfig builds the pgx configuration from the target. The DSN is parsed
// in memory only; parse failures return a generic error so a malformed
// connection string can never leak its password into a client message.
func newConnConfig(target domain.DBTarget) (*pgx.ConnConfig, error) {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(target.Username, target.Password),
		Host:   net.JoinHostPort(target.Host, strconv.Itoa(target.Port)),
		Path:   "/" + target.DatabaseName,
	}
	query := u.Query()
	sslmode := target.SSLMode
	if sslmode == "" {
		sslmode = domain.DBTargetSSLModeDisable
	}
	query.Set("sslmode", sslmode)
	u.RawQuery = query.Encode()

	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, errors.New("pgreadonly: invalid target connection parameters")
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	// Layer 1 of the read-only execution (spec §5.7): every transaction of this
	// connection is read-only by default.
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.ConnectTimeout = connectTimeout(target)
	return cfg, nil
}

// collectRows normalizes the result and enforces the row and byte caps. The
// first overflowing row is dropped and the result is marked truncated.
func collectRows(rows pgx.Rows, limits domain.QueryLimits) (domain.QueryResult, error) {
	if limits.MaxRows <= 0 {
		limits.MaxRows = domain.DBTargetDefaultMaxRows
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = application.MaxQueryResultBytes
	}
	fields := rows.FieldDescriptions()
	columns := make([]string, len(fields))
	for i := range fields {
		columns[i] = fields[i].Name
	}
	result := domain.QueryResult{Columns: columns, Rows: [][]string{}}
	consumed := 0
	for rows.Next() {
		if len(result.Rows) >= limits.MaxRows {
			result.Truncated = true
			break
		}
		values, err := rows.Values()
		if err != nil {
			return domain.QueryResult{}, queryFailed("failed to read the target result")
		}
		row := make([]string, len(values))
		rowBytes := 0
		for i, value := range values {
			cell := normalizeQueryValue(value)
			row[i] = cell
			rowBytes += len(cell)
		}
		if consumed+rowBytes > limits.MaxBytes {
			result.Truncated = true
			break
		}
		result.Rows = append(result.Rows, row)
		consumed += rowBytes
	}
	if err := rows.Err(); err != nil {
		return domain.QueryResult{}, classifyQueryError(err)
	}
	result.RowCount = len(result.Rows)
	return result, nil
}

// normalizeQueryValue renders one database value as the canonical string of
// spec §6.6.1: NULL is "null", booleans are true/false, numbers are plain
// decimal without trailing fractional zeros, timestamps are RFC3339 and the
// remaining values their text form.
func normalizeQueryValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return normalizeNumericText(strconv.FormatFloat(float64(typed), 'f', -1, 32))
	case float64:
		return normalizeNumericText(strconv.FormatFloat(typed, 'f', -1, 64))
	case string:
		return typed
	case []byte:
		return string(typed)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	case pgtype.Numeric:
		return normalizeNumeric(typed)
	case *pgtype.Numeric:
		if typed == nil {
			return "null"
		}
		return normalizeNumeric(*typed)
	case [16]byte:
		return formatUUID(typed)
	default:
		return fmt.Sprint(value)
	}
}

// normalizeNumeric renders a pgtype.Numeric through its text value.
func normalizeNumeric(numeric pgtype.Numeric) string {
	if !numeric.Valid {
		return "null"
	}
	value, err := numeric.Value()
	if err != nil {
		return "null"
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Sprint(value)
	}
	return normalizeNumericText(text)
}

// normalizeNumericText strips trailing fractional zeros from a plain decimal
// representation, per §6.6.1. NaN, infinities and exponent notation are left
// untouched.
func normalizeNumericText(text string) string {
	if text == "" || strings.ContainsAny(text, "eE") || strings.Contains(text, "Inf") || text == "NaN" {
		return text
	}
	if !strings.Contains(text, ".") {
		return text
	}
	trimmed := strings.TrimRight(text, "0")
	trimmed = strings.TrimSuffix(trimmed, ".")
	switch trimmed {
	case "", "-", "+", "-0", "+0":
		return "0"
	default:
		return trimmed
	}
}

// formatUUID renders a decoded uuid column as its canonical hyphenated form.
func formatUUID(value [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

// classifyQueryError maps a PostgreSQL or transport error onto the stable
// db_connection_failed/db_query_failed/db_readonly_violation reasons without
// echoing the statement or its result. A statement timeout (SQLSTATE 57014) and
// a canceled/expired request context are environment failures: the target or
// the connection could not serve the statement, so execution.db_assert prescribes
// blocked(environment) rather than inconclusive(case).
func classifyQueryError(err error) *domain.QueryError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "25006": // read_only_sql_transaction
			return domain.NewQueryError(domain.ReasonDBReadonlyViolation,
				"the statement modifies data but the transaction is read-only")
		case "42501": // insufficient_privilege
			return queryFailed("the target role is not allowed to run the statement")
		case "57014": // query_canceled (statement timeout or cancel)
			return connectionFailedWith("the statement exceeded the statement timeout")
		default:
			return queryFailed("the target database rejected the statement")
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return connectionFailedWith("the statement exceeded the statement timeout")
	}
	return queryFailed("the target database rejected the statement")
}

// connectionFailed classifies a connect/authentication/DNS failure.
func connectionFailed() *domain.QueryError {
	return connectionFailedWith("failed to connect to the target database")
}

// connectionFailedWith classifies an environment-side database failure with the
// given safe message.
func connectionFailedWith(message string) *domain.QueryError {
	return domain.NewQueryError(domain.ReasonDBConnectionFailed, message)
}

// queryFailed classifies a SQL execution failure.
func queryFailed(message string) *domain.QueryError {
	return domain.NewQueryError(domain.ReasonDBQueryFailed, message)
}

// failureResult builds a failed probe result with the elapsed latency.
func failureResult(started time.Time, reason, message string) domain.DBTargetTestResult {
	return domain.DBTargetTestResult{
		OK:        false,
		LatencyMS: time.Since(started).Milliseconds(),
		Warnings:  []string{},
		Error:     &domain.DBTargetTestError{Reason: reason, Message: message},
	}
}
