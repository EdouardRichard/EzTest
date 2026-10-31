package versionpg

import (
	"context"
	"fmt"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ListByVersion returns every logical requirement of one version ordered by ID
// ascending. It backs the version close requirement check, which needs all
// statuses (not just the clarified slice used by requirement closure).
func (r *RequirementRepository) ListByVersion(ctx context.Context, versionID int64) ([]version.Requirement, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+requirementColumns+` FROM requirement
		WHERE version_id = $1
		ORDER BY id
	`, versionID)
	if err != nil {
		return nil, fmt.Errorf("list version requirements: %w", err)
	}
	defer rows.Close()

	requirements := make([]version.Requirement, 0)
	for rows.Next() {
		requirement, err := scanRequirement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan version requirement: %w", err)
		}
		requirements = append(requirements, requirement)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list version requirements: %w", err)
	}
	return requirements, nil
}
