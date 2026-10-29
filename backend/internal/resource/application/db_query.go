package application

import (
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// Read-only query bounds of spec §5.7: a statement is at most 16 KiB and one
// result at most 1 MiB.
const (
	// MaxReadOnlySQLBytes is the maximum accepted SQL statement length.
	MaxReadOnlySQLBytes = 16 << 10
	// MaxQueryResultBytes is the maximum normalized result size of one query.
	MaxQueryResultBytes = 1 << 20
)

// allowedSQLFirstKeywords is the first-keyword whitelist of spec §5.7. The
// read-only transaction and the database role remain the last line of defence;
// this list is the first.
var allowedSQLFirstKeywords = map[string]struct{}{
	"select":  {},
	"with":    {},
	"show":    {},
	"explain": {},
}

// forbiddenSQLKeywords are the statement/database-admin keywords of spec §5.7.
// A read-only transaction already rejects the data-modifying ones at execution
// time; rejecting them up front keeps the failure reason deterministic.
var forbiddenSQLKeywords = map[string]struct{}{
	"copy": {}, "call": {}, "do": {}, "set": {}, "reset": {},
	"begin": {}, "commit": {}, "rollback": {},
	"create": {}, "alter": {}, "drop": {},
	"insert": {}, "update": {}, "delete": {}, "truncate": {},
	"grant": {}, "revoke": {}, "vacuum": {}, "analyze": {},
}

// ValidateReadOnlySQL enforces the static read-only gate of spec §5.7: length,
// single statement and the first-keyword/forbidden-keyword whitelist. It never
// executes anything and never inspects the database. A policy rejection is a
// db_readonly_violation; a malformed statement is a db_query_failed.
func ValidateReadOnlySQL(sql string) error {
	if len(sql) > MaxReadOnlySQLBytes {
		return readonlyViolation("the SQL statement must be at most 16 KiB")
	}
	tokens, multiple, err := scanSQL(sql)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return domain.NewQueryError(domain.ReasonDBQueryFailed, "the SQL statement is empty")
	}
	if _, ok := allowedSQLFirstKeywords[strings.ToLower(tokens[0])]; !ok {
		return readonlyViolation("only SELECT, WITH, SHOW and EXPLAIN statements are allowed")
	}
	if multiple {
		return readonlyViolation("only a single SQL statement is allowed")
	}
	for _, token := range tokens[1:] {
		if _, forbidden := forbiddenSQLKeywords[strings.ToLower(token)]; forbidden {
			return readonlyViolation("the SQL statement contains a write or administrative keyword")
		}
	}
	return nil
}

// readonlyViolation builds the stable policy rejection.
func readonlyViolation(message string) *domain.QueryError {
	return domain.NewQueryError(domain.ReasonDBReadonlyViolation, message)
}

// malformedSQL builds the stable malformed-statement rejection.
func malformedSQL(message string) *domain.QueryError {
	return domain.NewQueryError(domain.ReasonDBQueryFailed, message)
}

// QueryLimitsForTarget resolves the effective limits of one query: the request
// value wins, then the target configuration, then the platform defaults; the
// §5.7 caps are always enforced.
func QueryLimitsForTarget(target domain.DBTarget, limits domain.QueryLimits) domain.QueryLimits {
	out := limits
	if out.MaxRows <= 0 {
		out.MaxRows = target.MaxRows
	}
	if out.MaxRows <= 0 {
		out.MaxRows = domain.DBTargetDefaultMaxRows
	}
	if out.MaxRows > domain.DBTargetMaxRowsMax {
		out.MaxRows = domain.DBTargetMaxRowsMax
	}

	if out.TimeoutMS <= 0 {
		out.TimeoutMS = target.StatementTimeoutMS
	}
	if out.TimeoutMS <= 0 {
		out.TimeoutMS = domain.DBTargetDefaultStatementTimeoutMS
	}
	if out.TimeoutMS < domain.DBTargetStatementTimeoutMinMS {
		out.TimeoutMS = domain.DBTargetStatementTimeoutMinMS
	}
	if out.TimeoutMS > domain.DBTargetStatementTimeoutMaxMS {
		out.TimeoutMS = domain.DBTargetStatementTimeoutMaxMS
	}

	if out.MaxBytes <= 0 || out.MaxBytes > MaxQueryResultBytes {
		out.MaxBytes = MaxQueryResultBytes
	}
	return out
}

// scanSQL tokenizes the statement the way the whitelist needs it: it returns the
// word tokens (so keywords inside strings, quoted identifiers and comments are
// ignored) and reports whether content follows a statement terminator. An
// unterminated literal or comment is a malformed statement.
func scanSQL(sql string) ([]string, bool, error) {
	var tokens []string
	terminated := false
	multiple := false
	markContent := func() {
		if terminated {
			multiple = true
		}
	}

	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			i += 2
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			depth := 1
			i += 2
			for i < len(sql) && depth > 0 {
				switch {
				case i+1 < len(sql) && sql[i] == '/' && sql[i+1] == '*':
					depth++
					i += 2
				case i+1 < len(sql) && sql[i] == '*' && sql[i+1] == '/':
					depth--
					i += 2
				default:
					i++
				}
			}
			if depth > 0 {
				return nil, false, malformedSQL("the SQL statement has an unterminated block comment")
			}
		case c == '\'':
			markContent()
			escape := hasEscapeStringPrefix(sql, i)
			i++
			closed := false
			for i < len(sql) {
				if escape && sql[i] == '\\' {
					i += 2
					continue
				}
				if sql[i] == '\'' {
					if i+1 < len(sql) && sql[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, false, malformedSQL("the SQL statement has an unterminated string literal")
			}
		case c == '"':
			markContent()
			i++
			closed := false
			for i < len(sql) {
				if sql[i] == '"' {
					if i+1 < len(sql) && sql[i+1] == '"' {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, false, malformedSQL("the SQL statement has an unterminated quoted identifier")
			}
		case c == '$':
			if end, ok := dollarQuoteTagEnd(sql, i); ok {
				tag := sql[i:end]
				markContent()
				if closeAt := strings.Index(sql[end:], tag); closeAt >= 0 {
					i = end + closeAt + len(tag)
					continue
				}
				return nil, false, malformedSQL("the SQL statement has an unterminated dollar-quoted string")
			}
			markContent()
			i++
		case c == ';':
			terminated = true
			i++
		case isSQLIdentStart(c):
			markContent()
			start := i
			for i < len(sql) && isSQLIdentChar(sql[i]) {
				i++
			}
			tokens = append(tokens, sql[start:i])
		default:
			markContent()
			i++
		}
	}
	return tokens, multiple, nil
}

// hasEscapeStringPrefix reports whether the quote at index is the opening quote
// of a PostgreSQL E'...' escape string. The E must be a standalone prefix so an
// identifier ending in e does not turn the literal into an escape string.
func hasEscapeStringPrefix(sql string, index int) bool {
	if index == 0 || (sql[index-1] != 'e' && sql[index-1] != 'E') {
		return false
	}
	return index < 2 || !isSQLIdentChar(sql[index-2])
}

// dollarQuoteTagEnd returns the index just past the opening dollar-quote tag
// (for example $$ or $tag$) when sql[start:] begins one.
func dollarQuoteTagEnd(sql string, start int) (int, bool) {
	if start >= len(sql) || sql[start] != '$' {
		return 0, false
	}
	end := start + 1
	for end < len(sql) && isSQLDollarTagChar(sql[end]) {
		end++
	}
	if end < len(sql) && sql[end] == '$' {
		return end + 1, true
	}
	return 0, false
}

// isSQLDollarTagChar reports whether c can appear in a dollar-quote tag. The
// tag excludes the dollar sign itself, which would otherwise consume the
// closing delimiter of an empty tag ($$).
func isSQLDollarTagChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// isSQLIdentStart reports whether c can start an unquoted SQL identifier.
func isSQLIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// isSQLIdentChar reports whether c can continue an unquoted SQL identifier.
func isSQLIdentChar(c byte) bool {
	return isSQLIdentStart(c) || c >= '0' && c <= '9' || c == '$'
}
