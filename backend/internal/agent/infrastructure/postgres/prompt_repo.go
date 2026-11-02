package agentpg

import (
	"context"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// The runtime repository is the concrete implementation of both P6 ports; the
// assertions keep the assembly-root wiring honest.
var (
	_ agentapp.PromptRevisionSeeder   = (*RuntimeRepository)(nil)
	_ agentapp.ProfileAdminRepository = (*RuntimeRepository)(nil)
)

const promptRevisionColumns = `id, profile_key, version, system_prompt, note, source, actor_id, created_at`

// AppendPromptRevision appends one revision and mirrors the body into
// agent_profile.system_prompt in the same transaction. The profile row is
// locked so concurrent writers get distinct, increasing versions.
func (r *RuntimeRepository) AppendPromptRevision(
	ctx context.Context,
	profileKey, prompt, note, source string,
	actorID *int64,
) (int, error) {
	result, err := r.SavePromptRevision(ctx, agent.PromptWrite{
		ProfileKey:   profileKey,
		SystemPrompt: prompt,
		Note:         note,
		Source:       source,
		ActorID:      actorID,
	})
	if err != nil {
		return 0, err
	}
	return result.Version, nil
}

// SavePromptRevision is the checked prompt write used by the admin service:
// it supports no-op detection and an optional expected_version optimistic lock,
// both inside the same profile lock as the append.
func (r *RuntimeRepository) SavePromptRevision(ctx context.Context, write agent.PromptWrite) (agent.PromptWriteResult, error) {
	if err := write.Validate(); err != nil {
		return agent.PromptWriteResult{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agent.PromptWriteResult{}, fmt.Errorf("begin prompt write: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentPrompt string
	if err := tx.QueryRow(ctx, `
		SELECT system_prompt FROM agent_profile WHERE key = $1 FOR UPDATE
	`, write.ProfileKey).Scan(&currentPrompt); err != nil {
		return agent.PromptWriteResult{}, wrapDB("lock agent profile", err)
	}

	var currentVersion int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(version), 0)
		FROM agent_profile_prompt_revision
		WHERE profile_key = $1
	`, write.ProfileKey).Scan(&currentVersion); err != nil {
		return agent.PromptWriteResult{}, wrapDB("current prompt version", err)
	}

	if write.ExpectedVersion != nil && *write.ExpectedVersion != currentVersion {
		return agent.PromptWriteResult{}, agent.ErrPromptVersionConflict
	}
	if write.NoOpOnSame && currentPrompt == write.SystemPrompt {
		result := agent.PromptWriteResult{Version: currentVersion, Changed: false}
		if err := tx.Commit(ctx); err != nil {
			return agent.PromptWriteResult{}, fmt.Errorf("commit prompt no-op: %w", err)
		}
		return result, nil
	}

	version := currentVersion + 1
	var note *string
	if write.Note != "" {
		note = &write.Note
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_profile_prompt_revision
			(id, profile_key, version, system_prompt, note, source, actor_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
	`, r.ids.Next(), write.ProfileKey, version, write.SystemPrompt, note, write.Source, write.ActorID); err != nil {
		return agent.PromptWriteResult{}, wrapDB("insert prompt revision", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agent_profile
		SET system_prompt = $2, updated_at = now()
		WHERE key = $1
	`, write.ProfileKey, write.SystemPrompt); err != nil {
		return agent.PromptWriteResult{}, wrapDB("update profile prompt", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.PromptWriteResult{}, fmt.Errorf("commit prompt write: %w", err)
	}
	return agent.PromptWriteResult{Version: version, Changed: true}, nil
}

// EnsurePromptRevision inserts the v1 seed revision when the profile has no
// revision yet. It is idempotent and never overwrites current or historical
// administrator edits.
func (r *RuntimeRepository) EnsurePromptRevision(ctx context.Context, profileKey, prompt string) error {
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO agent_profile_prompt_revision
			(id, profile_key, version, system_prompt, note, source, actor_id, created_at)
		SELECT $1, $2, 1, $3, NULL, 'seed', NULL, now()
		WHERE NOT EXISTS (
			SELECT 1 FROM agent_profile_prompt_revision WHERE profile_key = $2
		)
	`, r.ids.Next(), profileKey, prompt); err != nil {
		return wrapDB("backfill prompt revision", err)
	}
	return nil
}

// GetPromptRevision loads one revision of a profile, or agent.ErrNotFound.
func (r *RuntimeRepository) GetPromptRevision(ctx context.Context, profileKey string, version int) (agent.PromptRevision, error) {
	revision, err := scanPromptRevision(r.pool.QueryRow(ctx, `
		SELECT `+promptRevisionColumns+`
		FROM agent_profile_prompt_revision
		WHERE profile_key = $1 AND version = $2
	`, profileKey, version))
	if err != nil {
		return agent.PromptRevision{}, wrapDB("get prompt revision", err)
	}
	return revision, nil
}

// ListPromptRevisions returns one version DESC page and reports whether an
// older page exists. beforeVersion is exclusive; limit defaults to 20 and is
// capped at 100.
func (r *RuntimeRepository) ListPromptRevisions(
	ctx context.Context,
	profileKey string,
	limit int,
	beforeVersion *int,
) ([]agent.PromptRevision, bool, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+promptRevisionColumns+`
		FROM agent_profile_prompt_revision
		WHERE profile_key = $1
		  AND ($2::int IS NULL OR version < $2)
		ORDER BY version DESC
		LIMIT $3
	`, profileKey, beforeVersion, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list prompt revisions: %w", err)
	}
	defer rows.Close()

	revisions := make([]agent.PromptRevision, 0, limit+1)
	for rows.Next() {
		revision, err := scanPromptRevision(rows)
		if err != nil {
			return nil, false, fmt.Errorf("scan prompt revision: %w", err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list prompt revisions: %w", err)
	}
	hasMore := len(revisions) > limit
	if hasMore {
		revisions = revisions[:limit]
	}
	return revisions, hasMore, nil
}

// LatestPromptVersions returns the current revision metadata of every profile
// that has a revision, in one query (admin profile list).
func (r *RuntimeRepository) LatestPromptVersions(ctx context.Context) (map[string]agent.PromptVersionInfo, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (profile_key) profile_key, version, created_at
		FROM agent_profile_prompt_revision
		ORDER BY profile_key, version DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("latest prompt versions: %w", err)
	}
	defer rows.Close()

	versions := make(map[string]agent.PromptVersionInfo)
	for rows.Next() {
		var profileKey string
		var info agent.PromptVersionInfo
		if err := rows.Scan(&profileKey, &info.Version, &info.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan prompt version: %w", err)
		}
		versions[profileKey] = info
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("latest prompt versions: %w", err)
	}
	return versions, nil
}

func scanPromptRevision(row rowScanner) (agent.PromptRevision, error) {
	var revision agent.PromptRevision
	var note *string
	if err := row.Scan(
		&revision.ID, &revision.ProfileKey, &revision.Version, &revision.SystemPrompt,
		&note, &revision.Source, &revision.ActorID, &revision.CreatedAt,
	); err != nil {
		return agent.PromptRevision{}, err
	}
	if note != nil {
		revision.Note = *note
	}
	return revision, nil
}
