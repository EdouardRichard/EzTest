// Package search adapts the mindmap context's case read to the P8 global-search
// provider port (spec §6.2/D55). It reads only case_node; the version display
// name is resolved through the version context's application service, so the
// mindmap context never reads the version table itself.
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

// Reads implements searchapp.Provider for the mindmap context: it owns the case
// result group (spec §3.3).
type Reads struct {
	q        queryer
	versions VersionReader
}

// New returns the mindmap-side global-search provider.
func New(pool *pgxpool.Pool, versions VersionReader) *Reads {
	return &Reads{q: pool, versions: versions}
}

var _ searchapp.Provider = (*Reads)(nil)

// Search runs the case ILIKE statement and returns the case group.
func (r *Reads) Search(ctx context.Context, keyword string, limit int) ([]searchapp.Group, error) {
	if r == nil || r.q == nil {
		return nil, errors.New("mindmap search reads: provider is not configured")
	}
	limit = normalizeLimit(limit)
	items, err := r.searchCases(ctx, likePattern(keyword), limit)
	if err != nil {
		return nil, err
	}
	return []searchapp.Group{{Type: searchapp.TypeCase, Items: items}}, nil
}

// caseRow is one case_node match before the version name is resolved.
type caseRow struct {
	id        int64
	title     string
	status    string
	versionID int64
}

// searchCases matches the case title, newest first, then resolves the version
// names through the version application service.
func (r *Reads) searchCases(ctx context.Context, pattern string, limit int) ([]searchapp.Item, error) {
	rows, err := r.q.Query(ctx, `
		SELECT n.id, n.title, n.status, n.version_id
		FROM case_node n
		WHERE n.kind = 'case' AND n.title ILIKE $1
		ORDER BY n.updated_at DESC, n.id DESC
		LIMIT $2`, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("mindmap search reads: cases: %w", err)
	}
	defer rows.Close()

	caseRows := make([]caseRow, 0)
	for rows.Next() {
		var row caseRow
		if err := rows.Scan(&row.id, &row.title, &row.status, &row.versionID); err != nil {
			return nil, fmt.Errorf("mindmap search reads: scan case: %w", err)
		}
		caseRows = append(caseRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mindmap search reads: cases: %w", err)
	}

	names, err := r.versionNames(ctx, caseRows)
	if err != nil {
		return nil, err
	}
	items := make([]searchapp.Item, 0, len(caseRows))
	for _, row := range caseRows {
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
			URL:         "/versions/" + versionIDText + "?tab=mindmap&case=" + idText,
		})
	}
	return items, nil
}

// versionNames resolves each distinct version id once through the version
// application service.
func (r *Reads) versionNames(ctx context.Context, rows []caseRow) (map[int64]string, error) {
	if len(rows) == 0 {
		return map[int64]string{}, nil
	}
	if r.versions == nil {
		return nil, errors.New("mindmap search reads: version name reader is not configured")
	}
	names := make(map[int64]string, len(rows))
	for _, row := range rows {
		if _, resolved := names[row.versionID]; resolved {
			continue
		}
		version, err := r.versions.Get(ctx, row.versionID)
		if err != nil {
			return nil, fmt.Errorf("mindmap search reads: version name %d: %w", row.versionID, err)
		}
		if version == nil {
			return nil, fmt.Errorf("mindmap search reads: version name %d: reader returned nil", row.versionID)
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
