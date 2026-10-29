// Package search adapts the version context's read-only version/requirement
// search to the P8 global-search provider port (spec §6.2/D55). It reads only
// the version context's own tables; aggregation, filtering and truncation belong
// to the search application service.
package search

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
)

// queryer is the narrow pgx surface of the provider. *pgxpool.Pool satisfies it;
// tests substitute a fake that serves canned rows.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Reads implements searchapp.Provider for the version context: it owns the
// version and requirement result groups (spec §3.3).
type Reads struct {
	q queryer
}

// New returns the version-side global-search provider.
func New(pool *pgxpool.Pool) *Reads {
	return &Reads{q: pool}
}

var _ searchapp.Provider = (*Reads)(nil)

// Search runs the two ILIKE statements of the version context and returns the
// version and requirement groups in display order.
func (r *Reads) Search(ctx context.Context, keyword string, limit int) ([]searchapp.Group, error) {
	if r == nil || r.q == nil {
		return nil, errors.New("version search reads: provider is not configured")
	}
	limit = normalizeLimit(limit)
	pattern := likePattern(keyword)

	versions, err := r.searchVersions(ctx, pattern, limit)
	if err != nil {
		return nil, err
	}
	requirements, err := r.searchRequirements(ctx, pattern, limit)
	if err != nil {
		return nil, err
	}
	return []searchapp.Group{
		{Type: searchapp.TypeVersion, Items: versions},
		{Type: searchapp.TypeRequirement, Items: requirements},
	}, nil
}

// searchVersions matches the version name and orders the newest version first.
func (r *Reads) searchVersions(ctx context.Context, pattern string, limit int) ([]searchapp.Item, error) {
	rows, err := r.q.Query(ctx, `
		SELECT id, name, label, status
		FROM version
		WHERE name ILIKE $1
		ORDER BY updated_at DESC, id DESC
		LIMIT $2`, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("version search reads: versions: %w", err)
	}
	defer rows.Close()

	items := make([]searchapp.Item, 0)
	for rows.Next() {
		var id int64
		var name, label, status string
		if err := rows.Scan(&id, &name, &label, &status); err != nil {
			return nil, fmt.Errorf("version search reads: scan version: %w", err)
		}
		idText := strconv.FormatInt(id, 10)
		items = append(items, searchapp.Item{
			ID:          idText,
			Title:       name,
			Subtitle:    label,
			VersionID:   idText,
			VersionName: name,
			Status:      status,
			URL:         "/versions/" + idText + "?tab=overview",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("version search reads: versions: %w", err)
	}
	return items, nil
}

// searchRequirements matches the requirement title and resolves its version
// display name from the version context's own table.
func (r *Reads) searchRequirements(ctx context.Context, pattern string, limit int) ([]searchapp.Item, error) {
	rows, err := r.q.Query(ctx, `
		SELECT r.id, r.title, r.status, r.version_id, v.name
		FROM requirement r
		JOIN version v ON v.id = r.version_id
		WHERE r.title ILIKE $1
		ORDER BY r.updated_at DESC, r.id DESC
		LIMIT $2`, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("version search reads: requirements: %w", err)
	}
	defer rows.Close()

	items := make([]searchapp.Item, 0)
	for rows.Next() {
		var id, versionID int64
		var title, status, versionName string
		if err := rows.Scan(&id, &title, &status, &versionID, &versionName); err != nil {
			return nil, fmt.Errorf("version search reads: scan requirement: %w", err)
		}
		idText := strconv.FormatInt(id, 10)
		versionIDText := strconv.FormatInt(versionID, 10)
		items = append(items, searchapp.Item{
			ID:          idText,
			Title:       title,
			Subtitle:    versionName,
			VersionID:   versionIDText,
			VersionName: versionName,
			Status:      status,
			URL:         "/versions/" + versionIDText + "?tab=requirements&requirement=" + idText,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("version search reads: requirements: %w", err)
	}
	return items, nil
}

// likePattern builds the ILIKE pattern with the user's LIKE meta characters
// escaped, so `%` and `_` match literally (PostgreSQL's default escape is the
// backslash).
func likePattern(keyword string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword)
	return "%" + escaped + "%"
}

// normalizeLimit keeps a direct provider call inside the documented bounds; the
// service already clamps, this is defence in depth.
func normalizeLimit(limit int) int {
	if limit <= 0 {
		return searchapp.DefaultLimit
	}
	if limit > searchapp.MaxLimit {
		return searchapp.MaxLimit
	}
	return limit
}
