// Package search adapts the delivery context's issue read to the P8 global-search
// provider port (spec §6.2/D55). It reads only the issue table; the version
// display name is resolved through the version context's application service, so
// the delivery context never reads the version table itself.
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
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// queryer is the narrow pgx surface of the provider. *pgxpool.Pool satisfies it;
// tests substitute a fake that serves canned rows.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// VersionReader is the narrow slice of the P3 version application service the
// adapter needs to resolve result version names. *version/application.VersionService
// satisfies it.
type VersionReader interface {
	Get(ctx context.Context, id int64) (*versionapp.Version, error)
}

// Reads implements searchapp.Provider for the delivery context: it owns the
// issue result group (spec §3.3).
type Reads struct {
	q        queryer
	versions VersionReader
}

// New returns the delivery-side global-search provider.
func New(pool *pgxpool.Pool, versions VersionReader) *Reads {
	return &Reads{q: pool, versions: versions}
}

var _ searchapp.Provider = (*Reads)(nil)

// Search runs the issue ILIKE statement and returns the issue group.
func (r *Reads) Search(ctx context.Context, keyword string, limit int) ([]searchapp.Group, error) {
	if r == nil || r.q == nil {
		return nil, errors.New("delivery search reads: provider is not configured")
	}
	limit = normalizeLimit(limit)
	items, err := r.searchIssues(ctx, likePattern(keyword), limit)
	if err != nil {
		return nil, err
	}
	return []searchapp.Group{{Type: searchapp.TypeIssue, Items: items}}, nil
}

// issueRow is one issue match before the version name is resolved. Only the
// public projection columns are read: the description, actual/repro fields and
// issue refs never enter the search result.
type issueRow struct {
	id        int64
	title     string
	status    string
	versionID int64
}

// searchIssues matches the issue title, newest first, then resolves the version
// names through the version application service.
func (r *Reads) searchIssues(ctx context.Context, pattern string, limit int) ([]searchapp.Item, error) {
	rows, err := r.q.Query(ctx, `
		SELECT id, title, status, version_id
		FROM issue
		WHERE title ILIKE $1
		ORDER BY updated_at DESC, id DESC
		LIMIT $2`, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("delivery search reads: issues: %w", err)
	}
	defer rows.Close()

	issueRows := make([]issueRow, 0)
	for rows.Next() {
		var row issueRow
		if err := rows.Scan(&row.id, &row.title, &row.status, &row.versionID); err != nil {
			return nil, fmt.Errorf("delivery search reads: scan issue: %w", err)
		}
		issueRows = append(issueRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("delivery search reads: issues: %w", err)
	}

	names, err := r.versionNames(ctx, issueRows)
	if err != nil {
		return nil, err
	}
	items := make([]searchapp.Item, 0, len(issueRows))
	for _, row := range issueRows {
		idText := strconv.FormatInt(row.id, 10)
		versionIDText := strconv.FormatInt(row.versionID, 10)
		versionName := names[row.versionID]
		items = append(items, searchapp.Item{
			ID:          idText,
			Title:       row.title,
			Subtitle:    versionName,
			VersionID:   versionIDText,
			VersionName: versionName,
			Status:      row.status,
			URL:         "/versions/" + versionIDText + "?tab=issues&issue=" + idText,
		})
	}
	return items, nil
}

// versionNames resolves each distinct version id once through the version
// application service.
func (r *Reads) versionNames(ctx context.Context, rows []issueRow) (map[int64]string, error) {
	if len(rows) == 0 {
		return map[int64]string{}, nil
	}
	if r.versions == nil {
		return nil, errors.New("delivery search reads: version name reader is not configured")
	}
	names := make(map[int64]string, len(rows))
	for _, row := range rows {
		if _, resolved := names[row.versionID]; resolved {
			continue
		}
		version, err := r.versions.Get(ctx, row.versionID)
		if err != nil {
			return nil, fmt.Errorf("delivery search reads: version name %d: %w", row.versionID, err)
		}
		if version == nil {
			return nil, fmt.Errorf("delivery search reads: version name %d: reader returned nil", row.versionID)
		}
		names[row.versionID] = version.Name
	}
	return names, nil
}

// likePattern builds the ILIKE pattern with the user's LIKE meta characters
// escaped, so `%` and `_` match literally.
func likePattern(keyword string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword)
	return "%" + escaped + "%"
}

// normalizeLimit keeps a direct provider call inside the documented bounds.
func normalizeLimit(limit int) int {
	if limit <= 0 {
		return searchapp.DefaultLimit
	}
	if limit > searchapp.MaxLimit {
		return searchapp.MaxLimit
	}
	return limit
}
