package db

import (
	"context"
	"embed"

	"github.com/jackc/pgx/v5/pgxpool"
)

// embeddedMigrations is the canonical migration set compiled into the binary.
// order.txt defines the global execution order; it stays empty until the first
// context migration is added.
//
//go:embed migrations/*
var embeddedMigrations embed.FS

// MigrateEmbedded applies the migrations embedded in the binary.
func MigrateEmbedded(ctx context.Context, pool *pgxpool.Pool) error {
	return Migrate(ctx, pool, embeddedMigrations)
}
