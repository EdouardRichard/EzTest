package adapters

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	dbQueryTimeout    = 10 * time.Second
	dbMaxSchemaTables = 200
	dbMaxColumns      = 200
	// dbDefaultStatementTimeout is the PG statement_timeout when the caller does
	// not pass statement_timeout_ms (5s).
	dbDefaultStatementTimeout = 5 * time.Second
	// dbCommitMargin is added to the configured statement timeout to derive the
	// outer context budget, so the context covers the statement AND the commit
	// without pre-empting the configured statement_timeout (T12 fix round 1: the
	// advertised 1..900000ms range must genuinely govern, not a fixed 10s ctx).
	dbCommitMargin = 5 * time.Second
)

// DatabaseAdapter runs general read-write SQL and schema summaries on the target
// PostgreSQL database. v0.2.4-C T12 (P8): the old READ ONLY transaction and the
// hard 200-row cap are lifted — queries run read-write and commit, rows are
// unbounded unless the caller passes MaxRows, and the statement timeout is
// configurable (default 5s). Credentials never leave this adapter.
type DatabaseAdapter struct{}

// NewDatabaseAdapter wires the target database adapter.
func NewDatabaseAdapter() *DatabaseAdapter { return &DatabaseAdapter{} }

var _ executionapp.DatabaseQuerier = (*DatabaseAdapter)(nil)

// Query executes one general SQL statement (read or write) and returns
// stringified rows plus the command tag / affected-row count for writes.
func (a *DatabaseAdapter) Query(ctx context.Context, dsn, username, password, sqlText string, opts executionapp.DBQueryOptions) (executionapp.DBQueryResult, error) {
	connection, err := connectTarget(ctx, dsn, username, password)
	if err != nil {
		return executionapp.DBQueryResult{}, err
	}
	defer func() { _ = connection.Close(context.Background()) }()

	statementTimeout := dbDefaultStatementTimeout
	if opts.StatementTimeoutMS > 0 {
		statementTimeout = time.Duration(opts.StatementTimeoutMS) * time.Millisecond
	}
	// T12 fix round 1: the outer context must NOT pre-empt the configured
	// statement_timeout (advertised 1..900000ms). Derive its budget from
	// max(floor, statementTimeout + commit margin) so a configured timeout above
	// the 10s floor genuinely governs and the commit is covered by the same
	// deadline. With no configured timeout the 10s floor holds (default 5s + 5s).
	budget := dbQueryTimeout
	if derived := statementTimeout + dbCommitMargin; derived > budget {
		budget = derived
	}
	queryCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	// P8: read-write transaction (the old pgx.ReadOnly rejected INSERT/UPDATE).
	tx, err := connection.BeginTx(queryCtx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		return executionapp.DBQueryResult{}, targetDBError("could not start a transaction", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	statementLimit := fmt.Sprintf("%dms", statementTimeout.Milliseconds())
	if _, err := tx.Exec(queryCtx, "SET LOCAL statement_timeout = '"+statementLimit+"'"); err != nil {
		return executionapp.DBQueryResult{}, targetDBError("could not set the statement timeout", err)
	}
	// P8: MaxRows <= 0 means unlimited (the old hard 200-row cap is lifted).
	maxRows := opts.MaxRows
	rows, err := tx.Query(queryCtx, sqlText)
	if err != nil {
		return executionapp.DBQueryResult{}, targetDBError("the SQL statement failed", err)
	}
	defer rows.Close()
	fields := rows.FieldDescriptions()
	result := executionapp.DBQueryResult{Columns: make([]string, 0, len(fields))}
	for _, field := range fields {
		result.Columns = append(result.Columns, field.Name)
	}
	for rows.Next() {
		if maxRows > 0 && len(result.Rows) >= maxRows {
			result.Truncated = true
			break
		}
		values, valueErr := rows.Values()
		if valueErr != nil {
			return executionapp.DBQueryResult{}, targetDBError("could not read the query result", valueErr)
		}
		row := make([]string, 0, len(values))
		for _, value := range values {
			if value == nil {
				row = append(row, "")
				continue
			}
			row = append(row, fmt.Sprint(value))
		}
		result.Rows = append(result.Rows, row)
	}
	if rows.Err() != nil {
		return executionapp.DBQueryResult{}, targetDBError("the query result could not be read", rows.Err())
	}
	result.RowCount = len(result.Rows)
	// Close the rows before committing: pgx keeps the connection busy while a
	// result set is open (an early max_rows break leaves it open), and CommandTag
	// is final once the rows are closed. The deferred Close is then a no-op.
	rows.Close()
	commandTag := rows.CommandTag()
	result.CommandTag = commandTag.String()
	result.RowsAffected = commandTag.RowsAffected()
	// P8: commit so write statements (INSERT/UPDATE/DELETE/DDL) persist. The
	// deferred Rollback is a no-op after a successful Commit.
	if err := tx.Commit(queryCtx); err != nil {
		return executionapp.DBQueryResult{}, targetDBError("could not commit the transaction", err)
	}
	return result, nil
}

// Schema returns a bounded tables/columns summary of one schema.
func (a *DatabaseAdapter) Schema(ctx context.Context, dsn, username, password, schema string) ([]executionapp.TableSchema, error) {
	connection, err := connectTarget(ctx, dsn, username, password)
	if err != nil {
		return nil, err
	}
	defer func() { _ = connection.Close(context.Background()) }()
	if strings.TrimSpace(schema) == "" {
		schema = "public"
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbQueryTimeout)
	defer cancel()
	tablesRows, err := connection.Query(queryCtx,
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = $1 AND table_type = 'BASE TABLE'
		 ORDER BY table_name LIMIT $2`, schema, dbMaxSchemaTables)
	if err != nil {
		return nil, targetDBError("could not read the database schema", err)
	}
	tableNames := make([]string, 0)
	for tablesRows.Next() {
		var name string
		if scanErr := tablesRows.Scan(&name); scanErr != nil {
			tablesRows.Close()
			return nil, targetDBError("could not read the database schema", scanErr)
		}
		tableNames = append(tableNames, name)
	}
	tablesRows.Close()
	if tablesRows.Err() != nil {
		return nil, targetDBError("could not read the database schema", tablesRows.Err())
	}
	tables := make([]executionapp.TableSchema, 0, len(tableNames))
	for _, table := range tableNames {
		tableSchema := executionapp.TableSchema{Schema: schema, Table: table}
		columnRows, columnErr := connection.Query(queryCtx,
			`SELECT column_name, data_type, is_nullable FROM information_schema.columns
			 WHERE table_schema = $1 AND table_name = $2
			 ORDER BY ordinal_position LIMIT $3`, schema, table, dbMaxColumns)
		if columnErr != nil {
			return nil, targetDBError("could not read the database schema", columnErr)
		}
		for columnRows.Next() {
			var name, dataType, nullable string
			if scanErr := columnRows.Scan(&name, &dataType, &nullable); scanErr != nil {
				columnRows.Close()
				return nil, targetDBError("could not read the database schema", scanErr)
			}
			tableSchema.Columns = append(tableSchema.Columns, executionapp.TableColumn{
				Name: name, Type: dataType, Nullable: strings.EqualFold(nullable, "YES"),
			})
		}
		columnRows.Close()
		if columnRows.Err() != nil {
			return nil, targetDBError("could not read the database schema", columnRows.Err())
		}
		tables = append(tables, tableSchema)
	}
	return tables, nil
}

func connectTarget(ctx context.Context, dsn, username, password string) (*pgx.Conn, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, platform.Invalid("database", "the target database DSN is invalid")
	}
	if strings.TrimSpace(username) != "" {
		config.User = username
	}
	if password != "" {
		config.Password = password
	}
	connectCtx, cancel := context.WithTimeout(ctx, dbQueryTimeout)
	defer cancel()
	connection, err := pgx.ConnectConfig(connectCtx, config)
	if err != nil {
		return nil, platform.NewError(503, "unavailable", "the target database is unreachable")
	}
	return connection, nil
}

func targetDBError(message string, cause error) error {
	return platform.NewError(500, "automation", message+": "+cause.Error())
}
