package codeintelpg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FactRepository stores immutable code_fact versions and their projection.
type FactRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
	now  func() time.Time
}

// NewFactRepository wires the fact repository.
func NewFactRepository(pool *pgxpool.Pool, ids platformid.Generator) *FactRepository {
	return &FactRepository{pool: pool, ids: ids, now: time.Now}
}

var _ codeintelapp.FactRepository = (*FactRepository)(nil)

const factColumns = `id, repo_id, commit_sha, fact_key, version, kind, path, name, signature,
	start_line, end_line, blob_hash, body, evidence, source, confidence, model, prompt_version,
	analysis_id, superseded_at, created_at, updated_at`

func scanFact(row rowScanner) (codeintel.Fact, error) {
	var fact codeintel.Fact
	var kind, confidence, source string
	var body, evidence []byte
	if err := row.Scan(&fact.ID, &fact.RepoID, &fact.CommitSHA, &fact.FactKey, &fact.Version,
		&kind, &fact.Path, &fact.Name, &fact.Signature, &fact.StartLine, &fact.EndLine,
		&fact.BlobHash, &body, &evidence, &source, &confidence, &fact.Model, &fact.PromptVersion,
		&fact.AnalysisID, &fact.SupersededAt, &fact.CreatedAt, &fact.UpdatedAt); err != nil {
		return codeintel.Fact{}, err
	}
	fact.Kind = codeintel.EntityKind(kind)
	fact.Source = source
	fact.Confidence = codeintel.Confidence(confidence)
	fact.Body = body
	fact.Evidence = evidence
	return fact, nil
}

// InsertVersion stores one immutable fact version. The current version is
// reused when the semantic body is unchanged; otherwise it is superseded and a
// new version+1 row is inserted (spec §5.2).
func (r *FactRepository) InsertVersion(ctx context.Context, fact codeintel.Fact) (codeintel.Fact, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return codeintel.Fact{}, false, wrapDB("begin fact version insert", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	version := 1
	var existingID int64
	var existingVersion int
	var existingBody []byte
	err = tx.QueryRow(ctx, `
		SELECT id, version, body FROM code_fact
		WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = $3
		ORDER BY version DESC
		LIMIT 1
		FOR UPDATE
	`, fact.RepoID, fact.CommitSHA, fact.FactKey).Scan(&existingID, &existingVersion, &existingBody)
	switch {
	case err == nil:
		if sameJSONBody(existingBody, fact.Body) {
			stored, loadErr := scanFact(tx.QueryRow(ctx, `SELECT `+factColumns+` FROM code_fact WHERE id = $1`, existingID))
			if loadErr != nil {
				return codeintel.Fact{}, false, wrapDB("load reused fact version", loadErr)
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return codeintel.Fact{}, false, wrapDB("commit reused fact version", commitErr)
			}
			return stored, true, nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE code_fact SET superseded_at = now(), updated_at = now() WHERE id = $1
		`, existingID); err != nil {
			return codeintel.Fact{}, false, wrapDB("supersede fact version", err)
		}
		version = existingVersion + 1
	case errors.Is(err, pgx.ErrNoRows):
		// First version.
	default:
		return codeintel.Fact{}, false, wrapDB("look up current fact version", err)
	}

	stored, err := scanFact(tx.QueryRow(ctx, `
		INSERT INTO code_fact (id, repo_id, commit_sha, fact_key, version, kind, path, name,
			signature, start_line, end_line, blob_hash, body, evidence, source, confidence,
			model, prompt_version, analysis_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb, $14::jsonb,
			$15, $16, $17, $18, $19, $20, $20)
		RETURNING `+factColumns,
		r.ids.Next(), fact.RepoID, fact.CommitSHA, fact.FactKey, version, string(fact.Kind),
		fact.Path, fact.Name, fact.Signature, fact.StartLine, fact.EndLine, fact.BlobHash,
		jsonText(fact.Body, `{}`), jsonText(fact.Evidence, `[]`), fact.Source,
		string(fact.Confidence), fact.Model, fact.PromptVersion, nullableInt64(fact.AnalysisID), r.now()))
	if err != nil {
		return codeintel.Fact{}, false, wrapDB("insert fact version", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return codeintel.Fact{}, false, wrapDB("commit fact version", err)
	}
	return stored, false, nil
}

// LatestByPath returns the current versions of one file's facts.
func (r *FactRepository) LatestByPath(ctx context.Context, repoID int64, commitSHA, filePath string) ([]codeintel.Fact, error) {
	return r.queryFacts(ctx, `
		SELECT `+factColumns+` FROM code_fact
		WHERE repo_id = $1 AND commit_sha = $2 AND path = $3 AND superseded_at IS NULL
		ORDER BY fact_key ASC
	`, repoID, commitSHA, filePath)
}

// LatestByCommit returns the current versions of the selected kinds.
func (r *FactRepository) LatestByCommit(ctx context.Context, repoID int64, commitSHA string, kinds []codeintel.EntityKind) ([]codeintel.Fact, error) {
	if len(kinds) == 0 {
		return r.queryFacts(ctx, `
			SELECT `+factColumns+` FROM code_fact
			WHERE repo_id = $1 AND commit_sha = $2 AND superseded_at IS NULL
			ORDER BY path ASC, fact_key ASC
		`, repoID, commitSHA)
	}
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return r.queryFacts(ctx, `
		SELECT `+factColumns+` FROM code_fact
		WHERE repo_id = $1 AND commit_sha = $2 AND superseded_at IS NULL
		  AND kind = ANY($3::text[])
		ORDER BY path ASC, fact_key ASC
	`, repoID, commitSHA, names)
}

// List pages one commit's current facts with an ID cursor.
func (r *FactRepository) List(ctx context.Context, repoID int64, commitSHA, kind, filePath, cursor string, limit int) ([]codeintel.Fact, string, bool, error) {
	args := []any{repoID, commitSHA}
	where := `repo_id = $1 AND commit_sha = $2 AND superseded_at IS NULL`
	if kind != "" {
		args = append(args, kind)
		where += fmt.Sprintf(` AND kind = $%d`, len(args))
	}
	if filePath != "" {
		args = append(args, filePath)
		where += fmt.Sprintf(` AND (path = $%d OR path LIKE $%d || '/%%')`, len(args), len(args))
	}
	if cursor != "" {
		args = append(args, cursor)
		where += fmt.Sprintf(` AND id > $%d`, len(args))
	}
	args = append(args, limit+1)
	rows, err := r.pool.Query(ctx, `SELECT `+factColumns+` FROM code_fact WHERE `+where+
		fmt.Sprintf(` ORDER BY id ASC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, "", false, wrapDB("list code facts", err)
	}
	defer rows.Close()

	facts := make([]codeintel.Fact, 0, limit)
	for rows.Next() {
		fact, scanErr := scanFact(rows)
		if scanErr != nil {
			return nil, "", false, wrapDB("scan code fact", scanErr)
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, wrapDB("iterate code facts", err)
	}
	hasMore := len(facts) > limit
	if hasMore {
		facts = facts[:limit]
	}
	nextCursor := ""
	if hasMore && len(facts) > 0 {
		nextCursor = platformid.Format(facts[len(facts)-1].ID)
	}
	return facts, nextCursor, hasMore, nil
}

// ByID loads one fact version.
func (r *FactRepository) ByID(ctx context.Context, id int64) (codeintel.Fact, error) {
	fact, err := scanFact(r.pool.QueryRow(ctx, `SELECT `+factColumns+` FROM code_fact WHERE id = $1`, id))
	if err != nil {
		return codeintel.Fact{}, wrapDB("get code fact", err)
	}
	return fact, nil
}

func (r *FactRepository) queryFacts(ctx context.Context, query string, args ...any) ([]codeintel.Fact, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDB("query code facts", err)
	}
	defer rows.Close()

	facts := make([]codeintel.Fact, 0)
	for rows.Next() {
		fact, scanErr := scanFact(rows)
		if scanErr != nil {
			return nil, wrapDB("scan code fact", scanErr)
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("iterate code facts", err)
	}
	return facts, nil
}

// sameJSONBody compares two JSON documents semantically. Malformed documents
// fall back to a byte comparison.
func sameJSONBody(left, right []byte) bool {
	if bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right)) {
		return true
	}
	var leftValue, rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}
	leftCanonical, err := json.Marshal(leftValue)
	if err != nil {
		return false
	}
	rightCanonical, err := json.Marshal(rightValue)
	if err != nil {
		return false
	}
	return bytes.Equal(leftCanonical, rightCanonical)
}
