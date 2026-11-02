package db

import "github.com/jackc/pgx/v5"

// pgxNoRows is kept behind a name so migrate.go does not need a second import
// alias and tests can reason about the sentinel.
var pgxNoRows = pgx.ErrNoRows
