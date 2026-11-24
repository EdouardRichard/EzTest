package designpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// JudgmentStore is the design PostgreSQL implementation of the judgment fact
// repository (design_0005). Both tables are append-only: this store never
// issues UPDATE or DELETE. The judge-exhausted escalation fact belongs to the
// version context's version_fact mechanism and is reached through the neutral
// JudgeExhaustedStore port, wired by the composition root; an unwired port
// fails closed instead of silently answering "no escalation".
type JudgmentStore struct {
	pool      *pgxpool.Pool
	exhausted designapp.JudgeExhaustedStore
}

var _ designapp.JudgmentRepository = (*JudgmentStore)(nil)

// NewJudgmentStore wraps the shared pool. exhausted may be nil only in
// assemblies that never touch the escalation path; the two escalation methods
// then return designapp.ErrJudgeExhaustedStoreUnavailable.
func NewJudgmentStore(pool *pgxpool.Pool, exhausted designapp.JudgeExhaustedStore) *JudgmentStore {
	return &JudgmentStore{pool: pool, exhausted: exhausted}
}

// judgmentFindingWire is the JSONB persistence shape of one finding. The json
// tags are the stored contract the HTTP/report readers decode.
type judgmentFindingWire struct {
	Dimension string `json:"dimension"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	CaseRef   string `json:"case_ref,omitempty"`
}

const judgmentSelect = `
	SELECT id, version_id, requirement_id, case_id, case_version_id, content_hash,
	       round, verdict, findings, ac_coverage_matrix, COALESCE(agent_run_id, 0), created_at
	FROM design_judgment
`

// InsertJudgment appends one judgment fact. A duplicate (case_id,
// case_version_id) maps to design.ErrDuplicate (409 at the API boundary).
func (s *JudgmentStore) InsertJudgment(ctx context.Context, judgment design.Judgment) error {
	findings, err := marshalFindings(judgment.Findings)
	if err != nil {
		return err
	}
	matrix := []byte("{}")
	if len(judgment.ACCoverageMatrix) > 0 {
		matrix, err = json.Marshal(judgment.ACCoverageMatrix)
		if err != nil {
			return fmt.Errorf("marshal judgment coverage matrix: %w", err)
		}
	}
	var createdAt *time.Time
	if !judgment.CreatedAt.IsZero() {
		value := judgment.CreatedAt.UTC()
		createdAt = &value
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO design_judgment (
			id, version_id, requirement_id, case_id, case_version_id, content_hash,
			round, verdict, findings, ac_coverage_matrix, agent_run_id, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11, COALESCE($12, now()))
	`, judgment.ID, judgment.VersionID, judgment.RequirementID, judgment.CaseID,
		judgment.CaseVersionID, judgment.ContentHash, judgment.Round, judgment.Verdict,
		findings, matrix, nullableID(judgment.AgentRunID), createdAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return design.ErrDuplicate
		}
		return fmt.Errorf("insert design judgment: %w", err)
	}
	return nil
}

// LatestJudgment returns the newest fact of one logical case, or nil.
func (s *JudgmentStore) LatestJudgment(ctx context.Context, caseID int64) (*design.Judgment, error) {
	row := s.pool.QueryRow(ctx, judgmentSelect+`
		WHERE case_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, caseID)
	judgment, err := scanJudgment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &judgment, nil
}

// ListJudgmentsByRequirement lists one requirement's facts in append order.
func (s *JudgmentStore) ListJudgmentsByRequirement(ctx context.Context, versionID, requirementID int64) ([]design.Judgment, error) {
	rows, err := s.pool.Query(ctx, judgmentSelect+`
		WHERE version_id = $1 AND requirement_id = $2
		ORDER BY created_at, id
	`, versionID, requirementID)
	if err != nil {
		return nil, fmt.Errorf("list design judgments: %w", err)
	}
	defer rows.Close()
	judgments := make([]design.Judgment, 0)
	for rows.Next() {
		judgment, err := scanJudgment(rows)
		if err != nil {
			return nil, err
		}
		judgments = append(judgments, judgment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan design judgments: %w", err)
	}
	return judgments, nil
}

// InsertOverride appends one human override audit row. The condition value
// set is validated against the DOMAIN authority before the DB CHECK sees it.
func (s *JudgmentStore) InsertOverride(ctx context.Context, override design.Override) error {
	if !design.ValidOverrideCondition(override.Condition) {
		return platform.Invalid("condition", "must be one of fail_exhausted, judge_exhausted")
	}
	var createdAt *time.Time
	if !override.CreatedAt.IsZero() {
		value := override.CreatedAt.UTC()
		createdAt = &value
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO design_judgment_override (
			id, case_id, case_version_id, content_hash, judgment_id, condition,
			actor_id, reason, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, COALESCE($9, now()))
	`, override.ID, override.CaseID, override.CaseVersionID, override.ContentHash,
		override.JudgmentID, override.Condition, override.ActorID, override.Reason, createdAt)
	if err != nil {
		return fmt.Errorf("insert design judgment override: %w", err)
	}
	return nil
}

// LatestOverride returns the newest override bound to exactly this case
// version, or nil.
func (s *JudgmentStore) LatestOverride(ctx context.Context, caseID, caseVersionID int64) (*design.Override, error) {
	var override design.Override
	err := s.pool.QueryRow(ctx, `
		SELECT id, case_id, case_version_id, content_hash, judgment_id, condition,
		       actor_id, reason, created_at
		FROM design_judgment_override
		WHERE case_id = $1 AND case_version_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, caseID, caseVersionID).Scan(
		&override.ID, &override.CaseID, &override.CaseVersionID, &override.ContentHash,
		&override.JudgmentID, &override.Condition, &override.ActorID, &override.Reason,
		&override.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read design judgment override: %w", err)
	}
	return &override, nil
}

// CountReworks derives rework_count = max(round) - 1 from the case's judgment
// facts (0 without facts). Round increments only on rework, so this is the
// rework count; agent work items are never read (controller ruling T6-1).
func (s *JudgmentStore) CountReworks(ctx context.Context, caseID int64) (int, error) {
	var count int
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(round), 1) - 1 FROM design_judgment WHERE case_id = $1
	`, caseID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count design reworks: %w", err)
	}
	return count, nil
}

// InsertJudgeExhausted delegates to the version-fact adapter (fail closed
// when unwired) and forwards its "newly created" flag unchanged: the caller
// emits the degradation event only on the creation transition.
func (s *JudgmentStore) InsertJudgeExhausted(ctx context.Context, versionID, requirementID, caseID, caseVersionID int64) (bool, error) {
	if s.exhausted == nil {
		return false, designapp.ErrJudgeExhaustedStoreUnavailable
	}
	return s.exhausted.InsertJudgeExhausted(ctx, versionID, requirementID, caseID, caseVersionID)
}

// HasJudgeExhausted delegates to the version-fact adapter (fail closed when
// unwired).
func (s *JudgmentStore) HasJudgeExhausted(ctx context.Context, caseID, caseVersionID int64) (bool, error) {
	if s.exhausted == nil {
		return false, designapp.ErrJudgeExhaustedStoreUnavailable
	}
	return s.exhausted.HasJudgeExhausted(ctx, caseID, caseVersionID)
}

// judgmentRowScanner is satisfied by pgx.Row and pgx.Rows.
type judgmentRowScanner interface {
	Scan(dest ...any) error
}

func scanJudgment(row judgmentRowScanner) (design.Judgment, error) {
	var judgment design.Judgment
	var findings, matrix []byte
	err := row.Scan(
		&judgment.ID, &judgment.VersionID, &judgment.RequirementID, &judgment.CaseID,
		&judgment.CaseVersionID, &judgment.ContentHash, &judgment.Round, &judgment.Verdict,
		&findings, &matrix, &judgment.AgentRunID, &judgment.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return design.Judgment{}, err
		}
		return design.Judgment{}, fmt.Errorf("scan design judgment: %w", err)
	}
	wire := make([]judgmentFindingWire, 0)
	if err := json.Unmarshal(findings, &wire); err != nil {
		return design.Judgment{}, fmt.Errorf("decode judgment findings: %w", err)
	}
	for _, finding := range wire {
		judgment.Findings = append(judgment.Findings, design.JudgmentFinding{
			Dimension: finding.Dimension,
			Severity:  finding.Severity,
			Message:   finding.Message,
			CaseRef:   finding.CaseRef,
		})
	}
	if err := json.Unmarshal(matrix, &judgment.ACCoverageMatrix); err != nil {
		return design.Judgment{}, fmt.Errorf("decode judgment coverage matrix: %w", err)
	}
	return judgment, nil
}

func marshalFindings(findings []design.JudgmentFinding) ([]byte, error) {
	wire := make([]judgmentFindingWire, 0, len(findings))
	for _, finding := range findings {
		wire = append(wire, judgmentFindingWire{
			Dimension: finding.Dimension,
			Severity:  finding.Severity,
			Message:   finding.Message,
			CaseRef:   finding.CaseRef,
		})
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal judgment findings: %w", err)
	}
	return encoded, nil
}

// nullableID maps the domain's zero-id "absent" convention onto a SQL NULL
// (design.OptionalID) — the store keeps no copy of the convention, and does not
// import the application layer for a four-line utility (H-F10 T6).
func nullableID(value int64) *int64 { return design.OptionalID(value) }
