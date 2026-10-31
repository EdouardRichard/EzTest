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
	dbStatementLimit  = "5s"
	dbMaxSchemaTables = 200
	dbMaxColumns      = 200
)

// DatabaseAdapter runs read-only queries and schema summaries on the target
// PostgreSQL database. Every query runs inside a READ ONLY transaction with a
// 5s statement timeout; credentials never leave this adapter.
type DatabaseAdapter struct{}

// NewDatabaseAdapter wires the target database adapter.
func NewDatabaseAdapter() *DatabaseAdapter { return &DatabaseAdapter{} }

var _ executionapp.DatabaseQuerier = (*DatabaseAdapter)(nil)

// Query executes one read-only SELECT/WITH and returns stringified rows.
func (a *DatabaseAdapter) Query(ctx context.Context, dsn, username, password, sqlText string, maxRows int) (executionapp.DBQueryResult, error) {
	connection, err := connectTarget(ctx, dsn, username, password)
	if err != nil {
		return executionapp.DBQueryResult{}, err
	}
	defer func() { _ = connection.Close(context.Background()) }()

	queryCtx, cancel := context.WithTimeout(ctx, dbQueryTimeout)
	defer cancel()
	tx, err := connection.BeginTx(queryCtx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return executionapp.DBQueryResult{}, targetDBError("could not start a read-only transaction", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(queryCtx, "SET LOCAL statement_timeout = '"+dbStatementLimit+"'"); err != nil {
		return executionapp.DBQueryResult{}, targetDBError("could not set the statement timeout", err)
	}
	if maxRows <= 0 || maxRows > 200 {
		maxRows = 200
	}
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
		if len(result.Rows) >= maxRows {
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
