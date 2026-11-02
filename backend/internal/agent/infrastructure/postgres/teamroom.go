package agentpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postColumns = `id, version_id, team, kind, content, refs, agent_run_id, created_by, created_at`

const digestColumns = `id, version_id, team, summary, pinned_post_ids, token_estimate, version, updated_at`

// TeamRoomRepository stores team_room_post and team_room_digest rows.
type TeamRoomRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

// NewTeamRoomRepository wires the team-room repository.
func NewTeamRoomRepository(pool *pgxpool.Pool, ids platformid.Generator) *TeamRoomRepository {
	return &TeamRoomRepository{pool: pool, ids: ids}
}

// EnsurePost inserts a post. Re-inserting the same primary key is a no-op that
// returns the stored row.
func (r *TeamRoomRepository) EnsurePost(ctx context.Context, post agent.TeamRoomPost) (agent.TeamRoomPost, error) {
	if err := post.Validate(); err != nil {
		return agent.TeamRoomPost{}, err
	}
	if post.ID <= 0 {
		post.ID = r.ids.Next()
	}
	refs := post.Refs
	if refs == nil {
		refs = []agent.PostRef{}
	}
	encodedRefs, err := encodeJSONArray("post refs", refs)
	if err != nil {
		return agent.TeamRoomPost{}, err
	}
	createdAt := timeOrNow(post.CreatedAt)

	stored, err := scanPost(r.pool.QueryRow(ctx, `
		INSERT INTO team_room_post (id, version_id, team, kind, content, refs,
			agent_run_id, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9)
		ON CONFLICT (id) DO NOTHING
		RETURNING `+postColumns,
		post.ID, post.VersionID, string(post.Team), string(post.Kind), post.Content,
		encodedRefs, post.AgentRunID, post.CreatedBy, createdAt))
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return agent.TeamRoomPost{}, wrapDB("insert team room post", err)
	}
	return r.getPost(ctx, post.ID)
}

// ListPosts returns the newest posts for a version/team pair.
func (r *TeamRoomRepository) ListPosts(ctx context.Context, versionID int64, team agent.Team, limit int) ([]agent.TeamRoomPost, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+postColumns+` FROM team_room_post
		WHERE version_id = $1 AND team = $2
		ORDER BY id DESC
		LIMIT $3
	`, versionID, string(team), limit)
	if err != nil {
		return nil, fmt.Errorf("list team room posts: %w", err)
	}
	return scanPosts(rows)
}

// ListPostsSince returns posts created strictly after since, oldest first.
func (r *TeamRoomRepository) ListPostsSince(ctx context.Context, versionID int64, team agent.Team, since time.Time, limit int) ([]agent.TeamRoomPost, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+postColumns+` FROM team_room_post
		WHERE version_id = $1 AND team = $2 AND created_at > $3
		ORDER BY id ASC
		LIMIT $4
	`, versionID, string(team), since, limit)
	if err != nil {
		return nil, fmt.Errorf("list team room posts since: %w", err)
	}
	return scanPosts(rows)
}

// CountPosts counts every post for a version/team pair.
func (r *TeamRoomRepository) CountPosts(ctx context.Context, versionID int64, team agent.Team) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM team_room_post WHERE version_id = $1 AND team = $2
	`, versionID, string(team)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count team room posts: %w", err)
	}
	return count, nil
}

// CountPostsSince counts posts created strictly after since.
func (r *TeamRoomRepository) CountPostsSince(ctx context.Context, versionID int64, team agent.Team, since time.Time) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM team_room_post
		WHERE version_id = $1 AND team = $2 AND created_at > $3
	`, versionID, string(team), since).Scan(&count); err != nil {
		return 0, fmt.Errorf("count team room posts since: %w", err)
	}
	return count, nil
}

// LatestDigest returns the newest digest, or agent.ErrNotFound.
func (r *TeamRoomRepository) LatestDigest(ctx context.Context, versionID int64, team agent.Team) (agent.TeamRoomDigest, error) {
	digest, err := scanDigest(r.pool.QueryRow(ctx, `
		SELECT `+digestColumns+` FROM team_room_digest
		WHERE version_id = $1 AND team = $2
		ORDER BY version DESC
		LIMIT 1
	`, versionID, string(team)))
	if err != nil {
		return agent.TeamRoomDigest{}, wrapDB("latest team room digest", err)
	}
	return digest, nil
}

// SaveDigest inserts one digest version.
func (r *TeamRoomRepository) SaveDigest(ctx context.Context, digest agent.TeamRoomDigest) (agent.TeamRoomDigest, error) {
	if err := digest.Validate(); err != nil {
		return agent.TeamRoomDigest{}, err
	}
	if digest.ID <= 0 {
		digest.ID = r.ids.Next()
	}
	pinned := digest.PinnedPostIDs
	if pinned == nil {
		pinned = []int64{}
	}
	encodedPinned, err := encodeJSONArray("pinned post ids", pinned)
	if err != nil {
		return agent.TeamRoomDigest{}, err
	}
	updatedAt := timeOrNow(digest.UpdatedAt)

	stored, err := scanDigest(r.pool.QueryRow(ctx, `
		INSERT INTO team_room_digest (id, version_id, team, summary, pinned_post_ids,
			token_estimate, version, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
		RETURNING `+digestColumns,
		digest.ID, digest.VersionID, string(digest.Team), digest.Summary, encodedPinned,
		digest.TokenEstimate, digest.Version, updatedAt))
	if err != nil {
		return agent.TeamRoomDigest{}, wrapDB("save team room digest", err)
	}
	return stored, nil
}

// PruneDigests keeps the newest keep digest versions for a version/team pair.
func (r *TeamRoomRepository) PruneDigests(ctx context.Context, versionID int64, team agent.Team, keep int) error {
	if keep <= 0 {
		return nil
	}
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM team_room_digest
		WHERE version_id = $1 AND team = $2
		  AND version <= COALESCE(
		        (SELECT max(version) FROM team_room_digest WHERE version_id = $1 AND team = $2), 0
		      ) - $3
	`, versionID, string(team), keep); err != nil {
		return fmt.Errorf("prune team room digests: %w", err)
	}
	return nil
}

func (r *TeamRoomRepository) getPost(ctx context.Context, id int64) (agent.TeamRoomPost, error) {
	post, err := scanPost(r.pool.QueryRow(ctx, `
		SELECT `+postColumns+` FROM team_room_post WHERE id = $1
	`, id))
	if err != nil {
		return agent.TeamRoomPost{}, wrapDB("get team room post", err)
	}
	return post, nil
}

func scanPosts(rows pgx.Rows) ([]agent.TeamRoomPost, error) {
	defer rows.Close()
	posts := make([]agent.TeamRoomPost, 0)
	for rows.Next() {
		post, err := scanPost(rows)
		if err != nil {
			return nil, fmt.Errorf("scan team room post: %w", err)
		}
		posts = append(posts, post)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list team room posts: %w", err)
	}
	return posts, nil
}

func scanPost(row rowScanner) (agent.TeamRoomPost, error) {
	var post agent.TeamRoomPost
	var team, kind string
	var refsRaw []byte
	if err := row.Scan(&post.ID, &post.VersionID, &team, &kind, &post.Content, &refsRaw,
		&post.AgentRunID, &post.CreatedBy, &post.CreatedAt); err != nil {
		return agent.TeamRoomPost{}, err
	}
	post.Team = agent.Team(team)
	post.Kind = agent.PostKind(kind)
	post.Refs = []agent.PostRef{}
	if len(refsRaw) > 0 {
		if err := json.Unmarshal(refsRaw, &post.Refs); err != nil {
			return agent.TeamRoomPost{}, fmt.Errorf("decode post refs: %w", err)
		}
	}
	return post, nil
}

func scanDigest(row rowScanner) (agent.TeamRoomDigest, error) {
	var digest agent.TeamRoomDigest
	var team string
	var pinnedRaw []byte
	if err := row.Scan(&digest.ID, &digest.VersionID, &team, &digest.Summary, &pinnedRaw,
		&digest.TokenEstimate, &digest.Version, &digest.UpdatedAt); err != nil {
		return agent.TeamRoomDigest{}, err
	}
	digest.Team = agent.Team(team)
	digest.PinnedPostIDs = []int64{}
	if len(pinnedRaw) > 0 {
		if err := json.Unmarshal(pinnedRaw, &digest.PinnedPostIDs); err != nil {
			return agent.TeamRoomDigest{}, fmt.Errorf("decode pinned post ids: %w", err)
		}
	}
	return digest, nil
}

var _ agent.TeamRoomRepository = (*TeamRoomRepository)(nil)
