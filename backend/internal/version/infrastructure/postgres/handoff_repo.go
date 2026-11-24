// Task 27 (v0.22 B §8/spec R-B5-1/R-B5-3): PostgreSQL adapters for the
// clarification handoff artifact and the clarification.search read model.
//
// This file implements three adapters over the version context's own tables:
//
//   - HandoffStore: the append-only clarification_handoff revision sequence
//     (DB-22, unique (version_id, revision)).
//   - HandoffReader: the version-owned half of the six-block read model. It joins
//     the requirement/fact/plan tables with the clarification question rows.
//     The requirements block is a V1 projection of requirement_version (T8/R-B1).
//     The resources block is NOT assembled here: H-B1 (v0.2.4-H) moved it into
//     the version application service, which reads the resource catalog through
//     resourceapp.HandoffResourceReader. This adapter only supplies the
//     version-owned scope (version.environment_id + enabled version_account rows)
//     via ResourceScope, so no resource-context table is read here and the
//     effective-scope rule is not re-implemented in SQL.
//   - ClarificationSearchStore: the clarification.search read port. It searches
//     structured Q/A, decision/constraint facts and bounded conversation-message
//     text snippets. It never returns whole raw chat, passwords or DSNs.
//
// No cross-context foreign key is added (Agent.md §6.9.7); the cross-context
// IDs are logical BIGINTs.

package versionpg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// handoffReadRowLimit bounds every per-table read before the application
	// layer applies its own (smaller) block caps.
	handoffReadRowLimit = 400
	// clarificationSearchRowLimit bounds each search subquery. The tool's own
	// limit caps the merged result.
	clarificationSearchRowLimit = 400
	// clarificationSnippetMax is the per-hit text bound of the search port.
	clarificationSnippetMax = 400
	// clarificationSearchSourceQAPrefix etc. are typed source references. The
	// scheme values live in versionapp (the producer); these are local aliases so
	// the reader and the open/decisions blocks share one source of truth.
	clarificationSourceQA          = versionapp.HandoffSourceQA
	clarificationSourceFact        = versionapp.HandoffSourceFact
	clarificationSourceMessage     = versionapp.HandoffSourceMessage
	clarificationSourceRequirement = versionapp.HandoffSourceRequirement
)

// ---------- handoff store ----------

// HandoffStore persists the clarification_handoff revision sequence.
type HandoffStore struct {
	pool *pgxpool.Pool
}

// NewHandoffStore wraps a pgx pool. Handoff IDs are assigned by the application
// service; plain inserts are idempotent by the unique (version_id, revision)
// key.
func NewHandoffStore(pool *pgxpool.Pool) *HandoffStore {
	return &HandoffStore{pool: pool}
}

var _ versionapp.HandoffStore = (*HandoffStore)(nil)

// Latest returns the newest handoff of one version, or the requested revision
// when revision > 0. A missing row/revision yields versionapp.ErrHandoffNotFound.
func (s *HandoffStore) Latest(ctx context.Context, versionID int64, revision int) (versionapp.HandoffRecord, error) {
	var record versionapp.HandoffRecord
	var content []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, content, created_at, updated_at
		FROM clarification_handoff
		WHERE version_id = $1 AND ($2 = 0 OR revision = $2)
		ORDER BY revision DESC
		LIMIT 1
	`, versionID, revision).Scan(&record.ID, &content, &record.CreatedAt, &record.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return versionapp.HandoffRecord{}, versionapp.ErrHandoffNotFound
		}
		return versionapp.HandoffRecord{}, fmt.Errorf("get clarification handoff: %w", err)
	}
	if err := json.Unmarshal(content, &record.Handoff); err != nil {
		return versionapp.HandoffRecord{}, fmt.Errorf("decode clarification handoff: %w", err)
	}
	if record.Handoff.VersionID == 0 {
		record.Handoff.VersionID = versionID
	}
	if record.Handoff.Revision == 0 {
		record.Handoff.Revision = revision
	}
	return record, nil
}

// Create inserts one handoff revision. A concurrent write of the same
// (version_id, revision) returns versionapp.ErrHandoffRevisionConflict so the
// service can refresh the latest revision and retry.
func (s *HandoffStore) Create(ctx context.Context, record versionapp.HandoffRecord) (versionapp.HandoffRecord, error) {
	if record.ID <= 0 {
		return versionapp.HandoffRecord{}, fmt.Errorf("create clarification handoff: missing id")
	}
	content, err := json.Marshal(record.Handoff)
	if err != nil {
		return versionapp.HandoffRecord{}, fmt.Errorf("encode clarification handoff: %w", err)
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO clarification_handoff (id, version_id, revision, content)
		VALUES ($1, $2, $3, $4::jsonb)
		RETURNING created_at, updated_at
	`, record.ID, record.Handoff.VersionID, record.Handoff.Revision, content).
		Scan(&record.CreatedAt, &record.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return versionapp.HandoffRecord{}, versionapp.ErrHandoffRevisionConflict
		}
		return versionapp.HandoffRecord{}, fmt.Errorf("insert clarification handoff: %w", err)
	}
	return record, nil
}

// ---------- handoff reader ----------

// HandoffReader reads the six clarification handoff blocks.
type HandoffReader struct {
	pool *pgxpool.Pool
}

// NewHandoffReader wraps a pgx pool.
func NewHandoffReader(pool *pgxpool.Pool) *HandoffReader {
	return &HandoffReader{pool: pool}
}

var _ versionapp.HandoffReader = (*HandoffReader)(nil)

// VersionExists reports whether the version row exists.
func (r *HandoffReader) VersionExists(ctx context.Context, versionID int64) (bool, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM version WHERE id = $1)`, versionID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check version exists: %w", err)
	}
	return exists, nil
}

// Requirements returns the current draft/clarified requirement revisions as a V1
// structural projection of requirement_version (R-B1, design §5.1): the user
// story, the structured acceptance-criteria array, sut_description and the
// optional rules/scope_out/account_roles_hint contract fields, plus priority,
// source_quote and confirmation time. Closed (historical) requirement rows are
// excluded: B §8.1 carries draft and clarified requirements only. Rows are read
// newest first so the application cap keeps the current clarification state.
func (r *HandoffReader) Requirements(ctx context.Context, versionID int64) ([]versionapp.HandoffRequirement, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, COALESCE(r.current_version_id, 0), rv.revision, rv.title,
		       rv.user_story, rv.rules, rv.acceptance_criteria, rv.priority,
		       rv.sut_description, rv.scope_out, rv.account_roles_hint, r.status,
		       r.source_quote, r.confidence, rc.confirmed_at
		FROM requirement r
		JOIN requirement_version rv ON rv.id = r.current_version_id
		LEFT JOIN requirement_confirmation rc ON rc.requirement_version_id = r.current_version_id
		WHERE r.version_id = $1 AND r.status = ANY($2::text[])
		ORDER BY r.id DESC
		LIMIT $3
	`, versionID, []string{string(version.RequirementDraft), string(version.RequirementClarified)}, handoffReadRowLimit)
	if err != nil {
		return nil, fmt.Errorf("list handoff requirements: %w", err)
	}
	defer rows.Close()

	items := make([]versionapp.HandoffRequirement, 0)
	for rows.Next() {
		var item versionapp.HandoffRequirement
		var userStoryRaw, criteriaRaw, rolesRaw []byte
		if err := rows.Scan(&item.ID, &item.CurrentVersionID, &item.Revision, &item.Title,
			&userStoryRaw, &item.Rules, &criteriaRaw, &item.Priority,
			&item.SutDescription, &item.ScopeOut, &rolesRaw, &item.Status,
			&item.SourceQuote, &item.Confidence, &item.ConfirmedAt); err != nil {
			return nil, fmt.Errorf("scan handoff requirement: %w", err)
		}
		item.UserStory = decodeHandoffUserStory(userStoryRaw)
		item.AcceptanceCriteria = decodeHandoffAcceptanceCriteria(criteriaRaw)
		item.AccountRolesHint = decodeHandoffStringSlice(rolesRaw)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan handoff requirements: %w", err)
	}
	return items, nil
}

// decodeHandoffUserStory decodes requirement_version.user_story into the handoff
// projection. A malformed/absent document yields the empty story rather than a
// scan failure (the column is NOT NULL DEFAULT '{}', so this is defence in depth).
func decodeHandoffUserStory(raw []byte) versionapp.HandoffUserStory {
	if len(raw) == 0 {
		return versionapp.HandoffUserStory{}
	}
	var story versionapp.HandoffUserStory
	if err := json.Unmarshal(raw, &story); err != nil {
		return versionapp.HandoffUserStory{}
	}
	return story
}

// decodeHandoffAcceptanceCriteria decodes requirement_version.acceptance_criteria
// into the handoff projection, carrying the server-assigned ids verbatim. The
// handoff never re-assigns ids: an extraction-produced id is not durable (T8 §13)
// and the stored revision is the single source.
func decodeHandoffAcceptanceCriteria(raw []byte) []versionapp.HandoffAcceptanceCriterion {
	if len(raw) == 0 {
		return []versionapp.HandoffAcceptanceCriterion{}
	}
	var criteria []versionapp.HandoffAcceptanceCriterion
	if err := json.Unmarshal(raw, &criteria); err != nil || criteria == nil {
		return []versionapp.HandoffAcceptanceCriterion{}
	}
	return criteria
}

// decodeHandoffStringSlice decodes a JSONB string array (account_roles_hint).
func decodeHandoffStringSlice(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return []string{}
	}
	return values
}

// handoffQuestionBatch is the subset of human_request.question the handoff
// needs. It is deliberately a local projection: the version context does not
// import the execution question domain.
type handoffQuestionBatch struct {
	Questions []struct {
		ID      string `json:"id"`
		Text    string `json:"text"`
		Options []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"options"`
		SourceRefs []string `json:"source_refs"`
	} `json:"questions"`
}

// handoffQuestionResponse is the human_request.response answer projection.
type handoffQuestionResponse struct {
	Answers []struct {
		QuestionID string   `json:"question_id"`
		Selected   []string `json:"selected"`
		Text       string   `json:"text"`
	} `json:"answers"`
	AnsweredAt string `json:"answered_at"`
}

// QA returns one HandoffQAItem per structured question of the version. Answered
// rows carry the selected options/free text and answer time; pending/expired
// rows are kept as unresolved (the service mirrors them into the open block).
func (r *HandoffReader) QA(ctx context.Context, versionID int64) ([]versionapp.HandoffQAItem, error) {
	requirementIndex, err := loadHandoffRequirementIndex(ctx, r.pool, versionID)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(message_id, 0), COALESCE(conversation_id, 0),
		       question, response, status, created_at, resolved_at
		FROM human_request
		WHERE version_id = $1 AND kind = $2
		ORDER BY id
		LIMIT $3
	`, versionID, string(execution.HumanClarificationQuestion), handoffReadRowLimit)
	if err != nil {
		return nil, fmt.Errorf("list handoff questions: %w", err)
	}
	defer rows.Close()

	items := make([]versionapp.HandoffQAItem, 0)
	for rows.Next() {
		var requestID, messageID, conversationID int64
		var questionRaw, responseRaw []byte
		var status string
		var askedAt time.Time
		var resolvedAt *time.Time
		if err := rows.Scan(&requestID, &messageID, &conversationID, &questionRaw,
			&responseRaw, &status, &askedAt, &resolvedAt); err != nil {
			return nil, fmt.Errorf("scan handoff question: %w", err)
		}
		var batch handoffQuestionBatch
		if err := json.Unmarshal(questionRaw, &batch); err != nil {
			return nil, fmt.Errorf("decode handoff question %d: %w", requestID, err)
		}
		var response handoffQuestionResponse
		if len(responseRaw) > 0 && string(responseRaw) != "{}" {
			if err := json.Unmarshal(responseRaw, &response); err != nil {
				return nil, fmt.Errorf("decode handoff question response %d: %w", requestID, err)
			}
		}
		answers := make(map[string]struct {
			selected []string
			text     string
		}, len(response.Answers))
		for _, answer := range response.Answers {
			answers[strings.TrimSpace(answer.QuestionID)] = struct {
				selected []string
				text     string
			}{selected: answer.Selected, text: answer.Text}
		}
		for _, question := range batch.Questions {
			item := versionapp.HandoffQAItem{
				RequestID:      requestID,
				MessageID:      messageID,
				ConversationID: conversationID,
				QuestionKey:    question.ID,
				Question:       question.Text,
				Status:         status,
				AskedAt:        askedAt,
			}
			for _, option := range question.Options {
				item.Options = append(item.Options, versionapp.HandoffQAOption{ID: option.ID, Label: option.Label})
			}
			item.SourceRefs = append(item.SourceRefs, question.SourceRefs...)
			// question.ask accepts arbitrary refs; the B §6.1 example uses
			// "requirement:FR-012". Resolve both the internal decimal id and
			// the version's requirement.external_id forms.
			item.RequirementRefs = handoffResolveRequirementRefs(question.SourceRefs, requirementIndex)
			if len(item.RequirementRefs) > 0 {
				item.RequirementID = item.RequirementRefs[0]
			}
			if answer, ok := answers[strings.TrimSpace(question.ID)]; ok {
				item.AnswerSelected = answer.selected
				item.AnswerText = answer.text
			}
			if status == string(execution.HumanAnswered) {
				if resolvedAt != nil {
					answeredAt := *resolvedAt
					item.AnsweredAt = &answeredAt
				} else if parsed, err := time.Parse(time.RFC3339, response.AnsweredAt); err == nil && !parsed.IsZero() {
					item.AnsweredAt = &parsed
				}
			}
			items = append(items, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan handoff questions: %w", err)
	}
	return items, nil
}

// Facts returns the current decision/constraint/gap/assumption/risk facts of
// the version (superseded rows excluded). Rows are read newest first so the
// application cap keeps the current state. The service projects decision and
// constraint rows into the decisions block and the rest into the open block.
func (r *HandoffReader) Facts(ctx context.Context, versionID int64) ([]versionapp.HandoffFact, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, kind, key, payload, confidence, source, source_ref, created_at
		FROM version_fact
		WHERE version_id = $1 AND superseded_by IS NULL
		  AND kind = ANY($2::text[])
		ORDER BY id DESC
		LIMIT $3
	`, versionID, []string{version.FactKindDecision, version.FactKindConstraint, version.FactKindGap,
		version.FactKindAssumption, version.FactKindRisk}, handoffReadRowLimit)
	if err != nil {
		return nil, fmt.Errorf("list handoff facts: %w", err)
	}
	defer rows.Close()

	items := make([]versionapp.HandoffFact, 0)
	for rows.Next() {
		var item versionapp.HandoffFact
		var payload []byte
		if err := rows.Scan(&item.ID, &item.Kind, &item.Key, &payload,
			&item.Confidence, &item.Source, &item.SourceRef, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan handoff fact: %w", err)
		}
		item.Summary = handoffFactSummary(item.Key, payload)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan handoff facts: %w", err)
	}
	return items, nil
}

// Plan returns the version stage plus the current and next non-terminal plan
// items in plan order.
func (r *HandoffReader) Plan(ctx context.Context, versionID int64) (versionapp.HandoffPlan, error) {
	var stage string
	if err := r.pool.QueryRow(ctx, `SELECT stage FROM version WHERE id = $1`, versionID).Scan(&stage); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return versionapp.HandoffPlan{}, fmt.Errorf("handoff plan: version %d not found", versionID)
		}
		return versionapp.HandoffPlan{}, fmt.Errorf("read handoff version stage: %w", err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, phase, title, status, decision
		FROM version_plan_item
		WHERE version_id = $1
		ORDER BY sort_order, id
		LIMIT $2
	`, versionID, handoffReadRowLimit)
	if err != nil {
		return versionapp.HandoffPlan{}, fmt.Errorf("list handoff plan items: %w", err)
	}
	defer rows.Close()

	plan := versionapp.HandoffPlan{Stage: stage}
	open := make([]versionapp.HandoffPlanItem, 0, 2)
	for rows.Next() {
		var item versionapp.HandoffPlanItem
		if err := rows.Scan(&item.ID, &item.Phase, &item.Title, &item.Status, &item.Decision); err != nil {
			return versionapp.HandoffPlan{}, fmt.Errorf("scan handoff plan item: %w", err)
		}
		if version.PlanItemTerminal(item.Status) {
			continue
		}
		open = append(open, item)
		if len(open) == 2 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return versionapp.HandoffPlan{}, fmt.Errorf("scan handoff plan items: %w", err)
	}
	if len(open) > 0 {
		plan.Current = &open[0]
	}
	if len(open) > 1 {
		plan.Next = &open[1]
	}
	return plan, nil
}

// ResourceScope returns the VERSION-OWNED input to the handoff resources block:
// the environment the version binds (version.environment_id) and the accounts it
// enabled (version_account.enabled), in account order.
//
// H-B1 (v0.2.4-H): this adapter reads ONLY version-owned tables. The resource
// catalog — the environment summary, the approved SUTs and their effective
// assets, the code pool and the account summaries — is read by the version
// APPLICATION service through resourceapp.HandoffResourceReader, so the
// effective-scope rule has one implementation (resourceapp.SUTView.IsApproved /
// EffectiveAssets) instead of a second one written as SQL status predicates here.
// A missing version row yields the zero scope (the caller has already checked
// VersionExists).
func (r *HandoffReader) ResourceScope(ctx context.Context, versionID int64) (versionapp.HandoffResourceScope, error) {
	var scope versionapp.HandoffResourceScope
	if err := r.pool.QueryRow(ctx, `
		SELECT environment_id FROM version WHERE id = $1
	`, versionID).Scan(&scope.EnvironmentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return versionapp.HandoffResourceScope{}, nil
		}
		return versionapp.HandoffResourceScope{}, fmt.Errorf("read handoff resource scope: %w", err)
	}
	// The enabled associations are read WITHOUT a row cut: the resource port
	// filters them by account status, so cutting the id list first would drop
	// active accounts behind a run of disabled ones (the former JOIN filtered
	// before its LIMIT). The set is version-scoped and small; the handoff's own
	// block cap and Truncated flag are the bounding authority (七.6: no silent
	// partial truth).
	rows, err := r.pool.Query(ctx, `
		SELECT account_id
		FROM version_account
		WHERE version_id = $1 AND enabled
		ORDER BY account_id
	`, versionID)
	if err != nil {
		return versionapp.HandoffResourceScope{}, fmt.Errorf("list handoff enabled accounts: %w", err)
	}
	defer rows.Close()
	scope.AccountIDs = make([]int64, 0)
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			return versionapp.HandoffResourceScope{}, fmt.Errorf("scan handoff enabled account: %w", err)
		}
		scope.AccountIDs = append(scope.AccountIDs, accountID)
	}
	if err := rows.Err(); err != nil {
		return versionapp.HandoffResourceScope{}, fmt.Errorf("scan handoff enabled accounts: %w", err)
	}
	return scope, nil
}

// handoffRequirementIndex resolves a version's requirement references. The
// structured-question format documented in B §6.1 is "requirement:FR-012"
// (external id), while older rows/tools use "requirement:<internal id>"; both
// forms point at the same version-scoped requirement.
type handoffRequirementIndex struct {
	byExternal map[string]int64
	byID       map[int64]string
}

// loadHandoffRequirementIndex reads the version's requirement external ids. The
// query stays scoped to the version, so an external id of another version can
// never resolve here.
func loadHandoffRequirementIndex(ctx context.Context, pool *pgxpool.Pool, versionID int64) (handoffRequirementIndex, error) {
	index := handoffRequirementIndex{
		byExternal: map[string]int64{},
		byID:       map[int64]string{},
	}
	rows, err := pool.Query(ctx, `
		SELECT id, external_id
		FROM requirement
		WHERE version_id = $1 AND external_id <> ''
		ORDER BY id
	`, versionID)
	if err != nil {
		return handoffRequirementIndex{}, fmt.Errorf("list requirement external ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var externalID string
		if err := rows.Scan(&id, &externalID); err != nil {
			return handoffRequirementIndex{}, fmt.Errorf("scan requirement external id: %w", err)
		}
		externalID = strings.TrimSpace(externalID)
		if id <= 0 || externalID == "" {
			continue
		}
		index.byExternal[externalID] = id
		// Case-insensitive fallback without overriding an exact match.
		if lower := strings.ToLower(externalID); lower != externalID {
			if _, exists := index.byExternal[lower]; !exists {
				index.byExternal[lower] = id
			}
		}
		index.byID[id] = externalID
	}
	if err := rows.Err(); err != nil {
		return handoffRequirementIndex{}, fmt.Errorf("scan requirement external ids: %w", err)
	}
	return index, nil
}

// resolveExternal maps an external requirement id to the version's internal id.
func (index handoffRequirementIndex) resolveExternal(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if id, ok := index.byExternal[raw]; ok && id > 0 {
		return id, true
	}
	if id, ok := index.byExternal[strings.ToLower(raw)]; ok && id > 0 {
		return id, true
	}
	return 0, false
}

// handoffRequirementRefID parses one "requirement:<id>" ref, accepting both the
// internal decimal id and the version's external id.
func handoffRequirementRefID(ref string, index handoffRequirementIndex) (int64, bool) {
	trimmed := strings.TrimSpace(ref)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, clarificationSourceRequirement) {
		return 0, false
	}
	raw := strings.TrimSpace(trimmed[len(clarificationSourceRequirement):])
	if raw == "" {
		return 0, false
	}
	if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
		return parsed, true
	}
	return index.resolveExternal(raw)
}

// handoffResolveRequirementRefs extracts requirement IDs from structured source
// references, resolving numeric and external forms and preserving order.
func handoffResolveRequirementRefs(refs []string, index handoffRequirementIndex) []int64 {
	ids := make([]int64, 0, len(refs))
	seen := map[int64]struct{}{}
	for _, ref := range refs {
		parsed, ok := handoffRequirementRefID(ref, index)
		if !ok {
			continue
		}
		if _, duplicate := seen[parsed]; duplicate {
			continue
		}
		seen[parsed] = struct{}{}
		ids = append(ids, parsed)
	}
	return ids
}

// handoffRequirementRefs is the numeric-only parser kept for callers/tests that
// have no version requirement index at hand.
func handoffRequirementRefs(refs []string) []int64 {
	return handoffResolveRequirementRefs(refs, handoffRequirementIndex{})
}

// decodeHandoffPayload decodes one JSONB payload without float64 precision
// loss: ids are snowflake int64s and exceed 2^53, so json.Number keeps them
// exact.
func decodeHandoffPayload(payload []byte) (map[string]any, error) {
	if len(payload) == 0 {
		return nil, errors.New("empty JSON payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	return object, nil
}

// handoffFactSummary renders the human-readable summary of one fact payload.
func handoffFactSummary(key string, payload []byte) string {
	object, err := decodeHandoffPayload(payload)
	if err != nil {
		return key
	}
	for _, field := range []string{"summary", "text", "value", "decision", "title"} {
		value, ok := object[field]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return typed
			}
		case json.Number:
			if typed.String() != "" {
				return typed.String()
			}
		case float64:
			return strconv.FormatFloat(typed, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(typed)
		}
	}
	return key
}

// ---------- clarification.search ----------

// ClarificationSearchStore is the SQL read model behind clarification.search
// (spec R-B5-3). It is version-scoped by construction: every subquery filters
// version_id from the injected scope query.
type ClarificationSearchStore struct {
	pool *pgxpool.Pool
}

// NewClarificationSearchStore wraps a pgx pool.
func NewClarificationSearchStore(pool *pgxpool.Pool) *ClarificationSearchStore {
	return &ClarificationSearchStore{pool: pool}
}

var _ agenttools.ClarificationSearcher = (*ClarificationSearchStore)(nil)

// SearchClarification searches the version's structured Q/A, decision/
// constraint facts and message summaries. It returns bounded snippets with
// sources only; whole raw messages are never returned. Every hit reports the
// version_id of the row it came from (never a value stamped from the query), so
// the tool's cross-version defense remains meaningful.
func (s *ClarificationSearchStore) SearchClarification(ctx context.Context, query agenttools.ClarificationSearchQuery) ([]agenttools.ClarificationSearchHit, error) {
	if query.VersionID <= 0 {
		return nil, fmt.Errorf("clarification search: version id must be positive")
	}
	limit := handoffSearchLimit(query.Limit)
	if len(query.Kinds) == 0 {
		// An omitted kind filter means every kind: mirrors the tool's
		// normalization so a direct adapter caller cannot accidentally search
		// nothing.
		query.Kinds = agenttools.ClarificationSearchKinds()
	}
	kinds := make(map[string]struct{}, len(query.Kinds))
	for _, kind := range query.Kinds {
		kinds[kind] = struct{}{}
	}
	// Requirement refs use the documented external form ("requirement:FR-012")
	// as well as internal decimal ids, so load the version's requirement
	// identity index: requirement-scoped searches filter by it and unfiltered
	// hits still report the resolved requirement id.
	requirementIndex, err := loadHandoffRequirementIndex(ctx, s.pool, query.VersionID)
	if err != nil {
		return nil, err
	}
	hits := make([]agenttools.ClarificationSearchHit, 0, limit)
	if _, ok := kinds[agenttools.ClarificationSearchKindQA]; ok {
		items, err := s.searchClarificationQA(ctx, query, requirementIndex, limit)
		if err != nil {
			return nil, err
		}
		hits = append(hits, items...)
	}
	factKinds := make([]string, 0, 2)
	// The request is matched against the TOOL's Host-facing result-kind contract
	// (agenttools.ClarificationSearchKind*), but the version_fact.kind filter is
	// sourced from the VERSION domain's fact vocabulary (version.FactKind*). The
	// two are separate constants on purpose: a retune of the tool contract must not
	// silently change which fact rows the search draws.
	for _, pair := range []struct{ toolKind, factKind string }{
		{agenttools.ClarificationSearchKindDecision, version.FactKindDecision},
		{agenttools.ClarificationSearchKindConstraint, version.FactKindConstraint},
	} {
		if _, ok := kinds[pair.toolKind]; ok {
			factKinds = append(factKinds, pair.factKind)
		}
	}
	if len(factKinds) > 0 {
		items, err := s.searchClarificationFacts(ctx, query, factKinds, requirementIndex, limit)
		if err != nil {
			return nil, err
		}
		hits = append(hits, items...)
	}
	if _, ok := kinds[agenttools.ClarificationSearchKindMessage]; ok {
		items, err := s.searchClarificationMessages(ctx, query, requirementIndex, limit)
		if err != nil {
			return nil, err
		}
		hits = append(hits, items...)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if !hits[i].Timestamp.Equal(hits[j].Timestamp) {
			return hits[i].Timestamp.After(hits[j].Timestamp)
		}
		if hits[i].Kind != hits[j].Kind {
			return hits[i].Kind < hits[j].Kind
		}
		if hits[i].QuestionID != hits[j].QuestionID {
			return hits[i].QuestionID < hits[j].QuestionID
		}
		return hits[i].MessageID < hits[j].MessageID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (s *ClarificationSearchStore) searchClarificationQA(ctx context.Context, query agenttools.ClarificationSearchQuery, requirementIndex handoffRequirementIndex, limit int) ([]agenttools.ClarificationSearchHit, error) {
	externalRequirementID := ""
	if query.RequirementID > 0 {
		externalRequirementID = requirementIndex.byID[query.RequirementID]
	}
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, h.version_id, COALESCE(h.message_id, 0), question, response, status,
		       h.created_at, h.resolved_at
		FROM human_request h
		WHERE h.version_id = $1 AND h.kind = $5
		  AND ($2 = '' OR h.question::text ILIKE '%' || $2 || '%'
		       OR h.response::text ILIKE '%' || $2 || '%')
		  AND ($3 = 0
		       OR strpos(lower(h.question::text), $6 || $3::text) > 0
		       OR ($4::text <> '' AND strpos(lower(h.question::text),
		            lower($6 || $4::text)) > 0))
		ORDER BY h.id DESC
		LIMIT $7
	`, query.VersionID, query.Query, query.RequirementID, externalRequirementID,
		string(execution.HumanClarificationQuestion), clarificationSourceRequirement, clarificationSearchRowLimit)
	if err != nil {
		return nil, fmt.Errorf("search clarification questions: %w", err)
	}
	defer rows.Close()

	needle := strings.ToLower(strings.TrimSpace(query.Query))
	hits := make([]agenttools.ClarificationSearchHit, 0, limit)
	for rows.Next() {
		var requestID, hitVersionID, messageID int64
		var questionRaw, responseRaw []byte
		var status string
		var askedAt time.Time
		var resolvedAt *time.Time
		if err := rows.Scan(&requestID, &hitVersionID, &messageID, &questionRaw, &responseRaw,
			&status, &askedAt, &resolvedAt); err != nil {
			return nil, fmt.Errorf("scan clarification question hit: %w", err)
		}
		var batch handoffQuestionBatch
		if err := json.Unmarshal(questionRaw, &batch); err != nil {
			return nil, fmt.Errorf("decode clarification question %d: %w", requestID, err)
		}
		var response handoffQuestionResponse
		if len(responseRaw) > 0 && string(responseRaw) != "{}" {
			if err := json.Unmarshal(responseRaw, &response); err != nil {
				return nil, fmt.Errorf("decode clarification question response %d: %w", requestID, err)
			}
		}
		answers := make(map[string]struct {
			selected []string
			text     string
		}, len(response.Answers))
		for _, answer := range response.Answers {
			answers[strings.TrimSpace(answer.QuestionID)] = struct {
				selected []string
				text     string
			}{selected: answer.Selected, text: answer.Text}
		}
		for _, question := range batch.Questions {
			refs := handoffResolveRequirementRefs(question.SourceRefs, requirementIndex)
			if query.RequirementID > 0 && !handoffContainsID(refs, query.RequirementID) {
				continue
			}
			snippet := handoffQAAnswerSnippet(question.Text, question.Options, answers[strings.TrimSpace(question.ID)])
			if needle != "" && !strings.Contains(strings.ToLower(snippet), needle) {
				continue
			}
			timestamp := askedAt
			if resolvedAt != nil && !resolvedAt.IsZero() {
				timestamp = *resolvedAt
			}
			hit := agenttools.ClarificationSearchHit{
				VersionID:  hitVersionID,
				Kind:       agenttools.ClarificationSearchKindQA,
				MessageID:  messageID,
				QuestionID: requestID,
				Snippet:    handoffBoundedSnippet(snippet, clarificationSnippetMax),
				Timestamp:  timestamp,
				Source:     clarificationSourceQA + strconv.FormatInt(requestID, 10),
			}
			if len(refs) > 0 {
				hit.RequirementID = refs[0]
			}
			if query.RequirementID > 0 {
				hit.RequirementID = query.RequirementID
			}
			hits = append(hits, hit)
			if len(hits) == limit {
				return hits, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan clarification question hits: %w", err)
	}
	return hits, nil
}

func (s *ClarificationSearchStore) searchClarificationFacts(ctx context.Context, query agenttools.ClarificationSearchQuery, kinds []string, requirementIndex handoffRequirementIndex, limit int) ([]agenttools.ClarificationSearchHit, error) {
	var sql string
	args := make([]any, 0, 6)
	if query.RequirementID > 0 {
		externalRequirementID := requirementIndex.byID[query.RequirementID]
		sql = `
			SELECT v.id, v.version_id, v.kind, v.key, v.payload, v.source_ref, v.created_at
			FROM version_fact v
			WHERE v.version_id = $1 AND v.superseded_by IS NULL
			  AND v.kind = ANY($2::text[])
			  AND ($3 = '' OR v.key ILIKE '%' || $3 || '%'
			       OR v.payload::text ILIKE '%' || $3 || '%')
			  AND (v.source_ref = $7 || ($4::bigint)::text
			       OR v.payload->>'requirement_id' = ($4::bigint)::text
			       OR v.payload->>'requirement' = ($4::bigint)::text
			       OR ($5::text <> '' AND (
			            v.source_ref = $7 || $5::text
			            OR v.payload->>'requirement_id' = $5::text
			            OR v.payload->>'requirement' = $5::text)))
			ORDER BY v.id DESC
			LIMIT $6
		`
		args = append(args, query.VersionID, kinds, query.Query, query.RequirementID,
			externalRequirementID, clarificationSearchRowLimit, clarificationSourceRequirement)
	} else {
		sql = `
			SELECT v.id, v.version_id, v.kind, v.key, v.payload, v.source_ref, v.created_at
			FROM version_fact v
			WHERE v.version_id = $1 AND v.superseded_by IS NULL
			  AND v.kind = ANY($2::text[])
			  AND ($3 = '' OR v.key ILIKE '%' || $3 || '%'
			       OR v.payload::text ILIKE '%' || $3 || '%')
			ORDER BY v.id DESC
			LIMIT $4
		`
		args = append(args, query.VersionID, kinds, query.Query, clarificationSearchRowLimit)
	}

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search clarification facts: %w", err)
	}
	defer rows.Close()

	needle := strings.ToLower(strings.TrimSpace(query.Query))
	hits := make([]agenttools.ClarificationSearchHit, 0, limit)
	for rows.Next() {
		var factID, hitVersionID int64
		var kind, key, sourceRef string
		var payload []byte
		var createdAt time.Time
		if err := rows.Scan(&factID, &hitVersionID, &kind, &key, &payload, &sourceRef, &createdAt); err != nil {
			return nil, fmt.Errorf("scan clarification fact hit: %w", err)
		}
		summary := handoffFactSummary(key, payload)
		if needle != "" && !strings.Contains(strings.ToLower(key+" "+summary+" "+string(payload)), needle) {
			continue
		}
		requirementID := handoffFactRequirementIDWithExternal(payload, requirementIndex)
		if requirementID == 0 {
			requirementID = handoffSourceRequirementIDWithExternal(sourceRef, requirementIndex)
		}
		if query.RequirementID > 0 && requirementID != query.RequirementID {
			continue
		}
		snippet := summary
		if key != "" && summary != key {
			snippet = key + "：" + summary
		}
		hits = append(hits, agenttools.ClarificationSearchHit{
			VersionID:     hitVersionID,
			Kind:          kind,
			RequirementID: requirementID,
			Snippet:       handoffBoundedSnippet(snippet, clarificationSnippetMax),
			Timestamp:     createdAt,
			Source:        clarificationSourceFact + strconv.FormatInt(factID, 10),
		})
		if len(hits) == limit {
			return hits, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan clarification fact hits: %w", err)
	}
	return hits, nil
}

func (s *ClarificationSearchStore) searchClarificationMessages(ctx context.Context, query agenttools.ClarificationSearchQuery, requirementIndex handoffRequirementIndex, limit int) ([]agenttools.ClarificationSearchHit, error) {
	externalRequirementID := ""
	if query.RequirementID > 0 {
		externalRequirementID = requirementIndex.byID[query.RequirementID]
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, c.version_id, m.parts, m.refs, m.created_at
		FROM conversation_message m
		JOIN conversation c ON c.id = m.conversation_id
		WHERE c.version_id = $1
		  AND c.kind = $6
		  AND ($2 = '' OR m.parts::text ILIKE '%' || $2 || '%')
		  AND ($3 = 0 OR EXISTS (
		      SELECT 1
		      FROM jsonb_array_elements(m.refs) AS ref
		      WHERE ref->>'kind' = $7
		        AND (ref->>'id' = $3::text
		             OR ($4::text <> '' AND ref->>'id' = $4::text))
		  ))
		ORDER BY m.id DESC
		LIMIT $5
	`, query.VersionID, query.Query, query.RequirementID, externalRequirementID,
		clarificationSearchRowLimit, version.ConversationClarification, version.MessageRefKindRequirement)
	if err != nil {
		return nil, fmt.Errorf("search clarification messages: %w", err)
	}
	defer rows.Close()

	needle := strings.ToLower(strings.TrimSpace(query.Query))
	hits := make([]agenttools.ClarificationSearchHit, 0, limit)
	for rows.Next() {
		var messageID, hitVersionID int64
		var partsRaw, refsRaw []byte
		var createdAt time.Time
		if err := rows.Scan(&messageID, &hitVersionID, &partsRaw, &refsRaw, &createdAt); err != nil {
			return nil, fmt.Errorf("scan clarification message hit: %w", err)
		}
		snippet := handoffMessageTextSnippet(partsRaw)
		if snippet == "" {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(snippet), needle) {
			continue
		}
		requirementIDs := handoffResolveMessageRequirementIDs(refsRaw, requirementIndex)
		requirementID := int64(0)
		if len(requirementIDs) > 0 {
			requirementID = requirementIDs[0]
		}
		if query.RequirementID > 0 {
			if !handoffContainsID(requirementIDs, query.RequirementID) {
				continue
			}
			requirementID = query.RequirementID
		}
		hits = append(hits, agenttools.ClarificationSearchHit{
			VersionID:     hitVersionID,
			Kind:          agenttools.ClarificationSearchKindMessage,
			RequirementID: requirementID,
			MessageID:     messageID,
			Snippet:       handoffBoundedSnippet(snippet, clarificationSnippetMax),
			Timestamp:     createdAt,
			Source:        clarificationSourceMessage + strconv.FormatInt(messageID, 10),
		})
		if len(hits) == limit {
			return hits, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan clarification message hits: %w", err)
	}
	return hits, nil
}

// handoffSearchLimit clamps a direct adapter caller's limit to the tool
// contract: <=0 means the default page, >max is clamped to max (never reset to
// the default, which would silently lose requested hits).
func handoffSearchLimit(requested int) int {
	if requested <= 0 {
		return agenttools.DefaultClarificationSearchLimit
	}
	if requested > agenttools.MaxClarificationSearchLimit {
		return agenttools.MaxClarificationSearchLimit
	}
	return requested
}

// handoffQAAnswerSnippet renders "question → answer" from the stored batch
// question, its options and the submitted answer.
func handoffQAAnswerSnippet(
	question string,
	options []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	},
	answer struct {
		selected []string
		text     string
	},
) string {
	builder := strings.Builder{}
	builder.WriteString(strings.TrimSpace(question))
	answerText := strings.TrimSpace(answer.text)
	if answerText == "" && len(answer.selected) > 0 {
		labels := make([]string, 0, len(answer.selected))
		labelsByID := make(map[string]string, len(options))
		for _, option := range options {
			labelsByID[option.ID] = option.Label
		}
		for _, selected := range answer.selected {
			if label := strings.TrimSpace(labelsByID[selected]); label != "" {
				labels = append(labels, label)
			} else {
				labels = append(labels, selected)
			}
		}
		answerText = strings.Join(labels, "、")
	}
	if answerText != "" {
		builder.WriteString(" → ")
		builder.WriteString(answerText)
	}
	return builder.String()
}

// handoffMessageTextSnippet concatenates only the text parts of one message and
// bounds the result. Tool arguments/results, thinking, attachments and job
// cards never enter the search snippet.
func handoffMessageTextSnippet(partsRaw []byte) string {
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(partsRaw, &parts); err != nil {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type != version.MessagePartText {
			continue
		}
		if trimmed := strings.TrimSpace(part.Text); trimmed != "" {
			texts = append(texts, trimmed)
		}
	}
	return strings.Join(texts, "\n")
}

// handoffMessageRequirementIDs extracts every numeric requirement reference of
// one conversation message. A message may be linked to several requirements.
func handoffMessageRequirementIDs(refsRaw []byte) []int64 {
	return handoffResolveMessageRequirementIDs(refsRaw, handoffRequirementIndex{})
}

// handoffResolveMessageRequirementIDs is handoffMessageRequirementIDs with
// external requirement ids resolved through the version requirement index.
func handoffResolveMessageRequirementIDs(refsRaw []byte, index handoffRequirementIndex) []int64 {
	var refs []struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(refsRaw, &refs); err != nil {
		return nil
	}
	ids := make([]int64, 0)
	seen := map[int64]struct{}{}
	for _, ref := range refs {
		if !strings.EqualFold(strings.TrimSpace(ref.Kind), version.MessageRefKindRequirement) {
			continue
		}
		raw := strings.TrimSpace(ref.ID)
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			resolved, ok := index.resolveExternal(raw)
			if !ok {
				continue
			}
			parsed = resolved
		}
		if _, duplicate := seen[parsed]; duplicate {
			continue
		}
		seen[parsed] = struct{}{}
		ids = append(ids, parsed)
	}
	return ids
}

// handoffFactRequirementID reads the requirement linkage of one fact payload
// (requirement_id or requirement).
func handoffFactRequirementID(payload []byte) int64 {
	return handoffFactRequirementIDWithExternal(payload, handoffRequirementIndex{})
}

// handoffFactRequirementIDWithExternal reads the fact's requirement linkage and
// resolves external requirement ids through the version requirement index.
func handoffFactRequirementIDWithExternal(payload []byte, index handoffRequirementIndex) int64 {
	object, err := decodeHandoffPayload(payload)
	if err != nil {
		return 0
	}
	for _, field := range []string{"requirement_id", "requirement"} {
		raw, ok := object[field]
		if !ok {
			continue
		}
		if parsed := handoffRequirementIDValue(raw, index); parsed > 0 {
			return parsed
		}
	}
	return 0
}

// handoffRequirementIDValue converts a payload id (decimal string, JSON number
// or external requirement id) to the internal requirement id.
func handoffRequirementIDValue(value any, index handoffRequirementIndex) int64 {
	switch typed := value.(type) {
	case string:
		raw := strings.TrimSpace(typed)
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			return parsed
		}
		if id, ok := index.resolveExternal(raw); ok {
			return id
		}
	case json.Number:
		if parsed, err := typed.Int64(); err == nil && parsed > 0 {
			return parsed
		}
	case float64:
		return int64(typed)
	case int64:
		return typed
	}
	return 0
}

// handoffSourceRequirementID parses a numeric "requirement:<id>" source ref.
func handoffSourceRequirementID(sourceRef string) int64 {
	return handoffSourceRequirementIDWithExternal(sourceRef, handoffRequirementIndex{})
}

// handoffSourceRequirementIDWithExternal parses "requirement:<id>" accepting the
// internal decimal id and the version's external id.
func handoffSourceRequirementIDWithExternal(sourceRef string, index handoffRequirementIndex) int64 {
	parsed, ok := handoffRequirementRefID(sourceRef, index)
	if !ok {
		return 0
	}
	return parsed
}

func handoffBoundedSnippet(value string, max int) string {
	trimmed := strings.TrimSpace(value)
	runes := []rune(trimmed)
	if len(runes) <= max {
		return trimmed
	}
	return string(runes[:max])
}

func handoffContainsID(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}
