// Package settingspg is the PostgreSQL adapter for the settings center.
package settingspg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository stores settings and append-only revisions.
type Repository struct {
	pool *pgxpool.Pool
}

// New wraps a pgx pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// List returns every setting.
func (r *Repository) List(ctx context.Context) ([]settings.Setting, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT key, value_json, category, type, is_secret, restart_required,
		       description, version, updated_by, updated_at
		FROM system_setting
		ORDER BY key
	`)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close()

	result := make([]settings.Setting, 0)
	for rows.Next() {
		item, err := scanSetting(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	return result, nil
}

// Get returns one setting.
func (r *Repository) Get(ctx context.Context, key string) (settings.Setting, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT key, value_json, category, type, is_secret, restart_required,
		       description, version, updated_by, updated_at
		FROM system_setting
		WHERE key = $1
	`, key)
	item, err := scanSetting(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings.Setting{}, settings.ErrNotFound
	}
	return item, err
}

// Seed inserts missing catalog defaults; existing rows are never overwritten.
func (r *Repository) Seed(ctx context.Context, definitions []settings.Definition) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin settings seed: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	for _, def := range definitions {
		if _, err := tx.Exec(ctx, `
			INSERT INTO system_setting
				(key, value_json, category, type, is_secret, restart_required, description, version)
			VALUES ($1, $2::jsonb, $3, $4, $5, $6, $7, 1)
			ON CONFLICT (key) DO NOTHING
		`, def.Key, string(def.Default), def.Category, string(def.Type), def.Secret, def.RestartRequired, def.Description); err != nil {
			return fmt.Errorf("seed setting %s: %w", def.Key, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit settings seed: %w", err)
	}
	return nil
}

// Update writes a revision and the new value atomically.
func (r *Repository) Update(ctx context.Context, key string, value json.RawMessage, revisionID, actorID int64, at time.Time) (settings.Setting, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return settings.Setting{}, fmt.Errorf("begin settings update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var oldValue []byte
	err = tx.QueryRow(ctx, `SELECT value_json FROM system_setting WHERE key = $1 FOR UPDATE`, key).Scan(&oldValue)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings.Setting{}, settings.ErrNotFound
	}
	if err != nil {
		return settings.Setting{}, fmt.Errorf("lock setting %s: %w", key, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO system_setting_revision (id, setting_key, old_value, new_value, actor_id, created_at)
		VALUES ($1, $2, $3::jsonb, $4::jsonb, $5, $6)
	`, revisionID, key, oldValue, string(value), actorID, at); err != nil {
		return settings.Setting{}, fmt.Errorf("insert setting revision: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE system_setting
		SET value_json = $2::jsonb, version = version + 1, updated_by = $3, updated_at = $4
		WHERE key = $1
	`, key, string(value), actorID, at); err != nil {
		return settings.Setting{}, fmt.Errorf("update setting: %w", err)
	}

	row := tx.QueryRow(ctx, `
		SELECT key, value_json, category, type, is_secret, restart_required,
		       description, version, updated_by, updated_at
		FROM system_setting
		WHERE key = $1
	`, key)
	item, err := scanSetting(row)
	if err != nil {
		return settings.Setting{}, fmt.Errorf("read updated setting: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return settings.Setting{}, fmt.Errorf("commit setting update: %w", err)
	}
	return item, nil
}

// Revisions returns change history, newest first.
func (r *Repository) Revisions(ctx context.Context, key string, limit int, beforeID *int64) ([]settings.Revision, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, setting_key, old_value, new_value, actor_id, created_at
		FROM system_setting_revision
		WHERE setting_key = $1 AND ($2::BIGINT IS NULL OR id < $2)
		ORDER BY id DESC
		LIMIT $3
	`, key, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list setting revisions: %w", err)
	}
	defer rows.Close()

	result := make([]settings.Revision, 0)
	for rows.Next() {
		var item settings.Revision
		var oldValue, newValue []byte
		var actorID *int64
		if err := rows.Scan(&item.ID, &item.Key, &oldValue, &newValue, &actorID, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan setting revision: %w", err)
		}
		item.OldValue = append(json.RawMessage(nil), oldValue...)
		item.NewValue = append(json.RawMessage(nil), newValue...)
		item.ActorID = actorID
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list setting revisions: %w", err)
	}
	return result, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSetting(row rowScanner) (settings.Setting, error) {
	var item settings.Setting
	var value []byte
	var settingType string
	if err := row.Scan(
		&item.Key, &value, &item.Category, &settingType, &item.Secret,
		&item.RestartRequired, &item.Description, &item.Version,
		&item.UpdatedBy, &item.UpdatedAt,
	); err != nil {
		return settings.Setting{}, err
	}
	item.Value = append(json.RawMessage(nil), value...)
	item.Type = settings.Type(settingType)
	return item, nil
}
