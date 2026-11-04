// Task 27 (v0.22 B §8/spec R-B5-1/R-B5-3): PostgreSQL adapters for the
// clarification handoff artifact and the clarification.search read model.
//
// This file implements three adapters over the version context's own tables:
//
//   - HandoffStore: the append-only clarification_handoff revision sequence
//     (DB-22, unique (version_id, revision)).
//   - HandoffReader: the six-block read model. It joins the requirement/fact/
//     plan tables with the clarification question rows and the resource tables.
//     Resource summaries use the v0.22 environment-level binding semantics
//     (version_sut.scope_exclude, version_account, sut_repository,
//     version_repository_override); Task 29 owns the resource service and UI.
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
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
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
	// clarificationSearchSourceQAPrefix etc. are typed source references.
	clarificationSourceQA      = "human_request:"
	clarificationSourceFact    = "version_fact:"
	clarificationSourceMessage = "conversation_message:"
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

// Requirements returns the current draft/clarified requirement revisions with
// acceptance criteria, priority, source_quote and confirmation time. Closed
// (historical) requirement rows are excluded: B §8.1 carries confirmed and
// draft requirements only. Rows are read newest first so the application cap
// keeps the current clarification state.
func (r *HandoffReader) Requirements(ctx context.Context, versionID int64) ([]versionapp.HandoffRequirement, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, COALESCE(r.current_version_id, 0), rv.revision, rv.title,
		       rv.description, rv.acceptance_criteria, rv.priority, r.status,
		       r.source_quote, r.confidence, rc.confirmed_at
		FROM requirement r
		JOIN requirement_version rv ON rv.id = r.current_version_id
		LEFT JOIN requirement_confirmation rc ON rc.requirement_version_id = r.current_version_id
		WHERE r.version_id = $1 AND r.status IN ('draft', 'clarified')
		ORDER BY r.id DESC
		LIMIT $2
	`, versionID, handoffReadRowLimit)
	if err != nil {
		return nil, fmt.Errorf("list handoff requirements: %w", err)
	}
	defer rows.Close()

	items := make([]versionapp.HandoffRequirement, 0)
	for rows.Next() {
		var item versionapp.HandoffRequirement
		if err := rows.Scan(&item.ID, &item.CurrentVersionID, &item.Revision, &item.Title,
			&item.Description, &item.AcceptanceCriteria, &item.Priority, &item.Status,
			&item.SourceQuote, &item.Confidence, &item.ConfirmedAt); err != nil {
			return nil, fmt.Errorf("scan handoff requirement: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan handoff requirements: %w", err)
	}
	return items, nil
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
		WHERE version_id = $1 AND kind = 'clarification_question'
		ORDER BY id
		LIMIT $2
	`, versionID, handoffReadRowLimit)
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
			if status == "answered" {
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
		  AND kind IN ('decision', 'constraint', 'gap', 'assumption', 'risk')
		ORDER BY id DESC
		LIMIT $2
	`, versionID, handoffReadRowLimit)
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
		if handoffPlanItemTerminal(item.Status) {
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

// Resources returns the version's bound SUT/page/API/account/database/repository
// summaries. Pages/APIs/databases honor the v0.22 version_sut.scope_exclude
// lists; accounts are the enabled version_account rows and repositories are the
// SUT-inherited set with version overrides applied. Passwords, DSNs and account
// notes never enter the handoff.
func (r *HandoffReader) Resources(ctx context.Context, versionID int64) (versionapp.HandoffResources, error) {
	resources := versionapp.HandoffResources{
		SUTs:         []versionapp.HandoffSUT{},
		Pages:        []versionapp.HandoffPage{},
		APIs:         []versionapp.HandoffAPI{},
		Accounts:     []versionapp.HandoffAccount{},
		Databases:    []versionapp.HandoffDatabase{},
		Repositories: []versionapp.HandoffRepository{},
	}
	bindings, err := r.handoffBindings(ctx, versionID)
	if err != nil {
		return versionapp.HandoffResources{}, err
	}
	pageSeen := map[int64]struct{}{}
	apiSeen := map[int64]struct{}{}
	databaseSeen := map[int64]struct{}{}
	for _, binding := range bindings {
		resources.SUTs = append(resources.SUTs, binding.summary)
		pages, err := r.handoffPages(ctx, binding)
		if err != nil {
			return versionapp.HandoffResources{}, err
		}
		for _, page := range pages {
			if _, duplicate := pageSeen[page.ID]; duplicate {
				continue
			}
			pageSeen[page.ID] = struct{}{}
			resources.Pages = append(resources.Pages, page)
		}
		apis, err := r.handoffAPIs(ctx, binding)
		if err != nil {
			return versionapp.HandoffResources{}, err
		}
		for _, api := range apis {
			if _, duplicate := apiSeen[api.ID]; duplicate {
				continue
			}
			apiSeen[api.ID] = struct{}{}
			resources.APIs = append(resources.APIs, api)
		}
		databases, err := r.handoffDatabases(ctx, binding)
		if err != nil {
			return versionapp.HandoffResources{}, err
		}
		for _, database := range databases {
			if _, duplicate := databaseSeen[database.ID]; duplicate {
				continue
			}
			databaseSeen[database.ID] = struct{}{}
			resources.Databases = append(resources.Databases, database)
		}
	}
	accounts, err := r.handoffAccounts(ctx, versionID)
	if err != nil {
		return versionapp.HandoffResources{}, err
	}
	resources.Accounts = accounts
	repositories, err := r.handoffRepositories(ctx, versionID)
	if err != nil {
		return versionapp.HandoffResources{}, err
	}
	resources.Repositories = repositories
	return resources, nil
}

// handoffScopeExclude is the v0.22 version_sut.scope_exclude JSON shape. Empty
// lists mean the whole environment is in scope (DB-11).
type handoffScopeExclude struct {
	Pages     []int64 `json:"pages"`
	APIs      []int64 `json:"apis"`
	Databases []int64 `json:"databases"`
}

type handoffBinding struct {
	summary  versionapp.HandoffSUT
	excluded handoffScopeExclude
}

func (r *HandoffReader) handoffBindings(ctx context.Context, versionID int64) ([]handoffBinding, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT vs.id, vs.sut_id, s.name, vs.environment_id, e.name, e.base_url,
		       vs.scope_exclude
		FROM version_sut vs
		JOIN sut s ON s.id = vs.sut_id
		JOIN sut_environment e ON e.id = vs.environment_id
		WHERE vs.version_id = $1 AND vs.status = 'active'
		ORDER BY vs.id
		LIMIT $2
	`, versionID, handoffReadRowLimit)
	if err != nil {
		return nil, fmt.Errorf("list handoff version SUTs: %w", err)
	}
	defer rows.Close()

	bindings := make([]handoffBinding, 0)
	for rows.Next() {
		var binding handoffBinding
		var scopeRaw []byte
		if err := rows.Scan(&binding.summary.VersionSUTID, &binding.summary.SUTID,
			&binding.summary.Name, &binding.summary.EnvironmentID,
			&binding.summary.Environment, &binding.summary.BaseURL, &scopeRaw); err != nil {
			return nil, fmt.Errorf("scan handoff version SUT: %w", err)
		}
		if len(scopeRaw) > 0 {
			// A malformed historical scope is treated as "nothing excluded".
			_ = json.Unmarshal(scopeRaw, &binding.excluded)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan handoff version SUTs: %w", err)
	}
	return bindings, nil
}

func (r *HandoffReader) handoffPages(ctx context.Context, binding handoffBinding) ([]versionapp.HandoffPage, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, url, route_template
		FROM sut_page
		WHERE sut_id = $1 AND environment_id = $2 AND status = 'approved'
		ORDER BY id
		LIMIT $3
	`, binding.summary.SUTID, binding.summary.EnvironmentID, handoffReadRowLimit)
	if err != nil {
		return nil, fmt.Errorf("list handoff pages: %w", err)
	}
	defer rows.Close()
	excluded := handoffIDSet(binding.excluded.Pages)
	pages := make([]versionapp.HandoffPage, 0)
	for rows.Next() {
		var page versionapp.HandoffPage
		if err := rows.Scan(&page.ID, &page.Name, &page.URL, &page.RouteTemplate); err != nil {
			return nil, fmt.Errorf("scan handoff page: %w", err)
		}
		if _, skip := excluded[page.ID]; skip {
			continue
		}
		page.VersionSUTID = binding.summary.VersionSUTID
		pages = append(pages, page)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan handoff pages: %w", err)
	}
	return pages, nil
}

func (r *HandoffReader) handoffAPIs(ctx context.Context, binding handoffBinding) ([]versionapp.HandoffAPI, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, method, path_template
		FROM sut_api
		WHERE sut_id = $1 AND environment_id = $2 AND status = 'approved'
		ORDER BY id
		LIMIT $3
	`, binding.summary.SUTID, binding.summary.EnvironmentID, handoffReadRowLimit)
	if err != nil {
		return nil, fmt.Errorf("list handoff APIs: %w", err)
	}
	defer rows.Close()
	excluded := handoffIDSet(binding.excluded.APIs)
	apis := make([]versionapp.HandoffAPI, 0)
	for rows.Next() {
		var api versionapp.HandoffAPI
		if err := rows.Scan(&api.ID, &api.Name, &api.Method, &api.Path); err != nil {
			return nil, fmt.Errorf("scan handoff API: %w", err)
		}
		if _, skip := excluded[api.ID]; skip {
			continue
		}
		api.VersionSUTID = binding.summary.VersionSUTID
		apis = append(apis, api)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan handoff APIs: %w", err)
	}
	return apis, nil
}

func (r *HandoffReader) handoffDatabases(ctx context.Context, binding handoffBinding) ([]versionapp.HandoffDatabase, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, kind, read_only
		FROM target_database
		WHERE sut_id = $1 AND environment_id = $2 AND status = 'active'
		ORDER BY id
		LIMIT $3
	`, binding.summary.SUTID, binding.summary.EnvironmentID, handoffReadRowLimit)
	if err != nil {
		return nil, fmt.Errorf("list handoff databases: %w", err)
	}
	defer rows.Close()
	excluded := handoffIDSet(binding.excluded.Databases)
	databases := make([]versionapp.HandoffDatabase, 0)
	for rows.Next() {
		var database versionapp.HandoffDatabase
		if err := rows.Scan(&database.ID, &database.Name, &database.Kind, &database.ReadOnly); err != nil {
			return nil, fmt.Errorf("scan handoff database: %w", err)
		}
		if _, skip := excluded[database.ID]; skip {
			continue
		}
		databases = append(databases, database)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan handoff databases: %w", err)
	}
	return databases, nil
}

func (r *HandoffReader) handoffAccounts(ctx context.Context, versionID int64) ([]versionapp.HandoffAccount, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.username, a.roles
		FROM version_account va
		JOIN test_account a ON a.id = va.account_id
		WHERE va.version_id = $1 AND va.enabled AND a.status = 'active'
		ORDER BY a.id
		LIMIT $2
	`, versionID, handoffReadRowLimit)
	if err != nil {
		return nil, fmt.Errorf("list handoff accounts: %w", err)
	}
	defer rows.Close()
	accounts := make([]versionapp.HandoffAccount, 0)
	for rows.Next() {
		var account versionapp.HandoffAccount
		var roles []byte
		if err := rows.Scan(&account.ID, &account.Username, &roles); err != nil {
			return nil, fmt.Errorf("scan handoff account: %w", err)
		}
		if len(roles) > 0 {
			_ = json.Unmarshal(roles, &account.Roles)
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan handoff accounts: %w", err)
	}
	return accounts, nil
}

// handoffRepositories merges the SUT-inherited repositories with the version
// overrides: included=false removes an inherited row, included=true adds one.
func (r *HandoffReader) handoffRepositories(ctx context.Context, versionID int64) ([]versionapp.HandoffRepository, error) {
	repositories := make(map[int64]versionapp.HandoffRepository)
	// A repository may be inherited from two SUTs with different roles. Pick
	// one role deterministically (the lexicographically smallest) instead of
	// letting SELECT DISTINCT + ORDER BY r.id return whichever row the plan
	// happens to emit: that would flip the handoff signature across
	// regenerations. sr.id is the final tie-breaker.
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (r.id) r.id, r.name, r.branch, sr.role
		FROM version_sut vs
		JOIN sut_repository sr ON sr.sut_id = vs.sut_id AND sr.enabled
		JOIN code_repository r ON r.id = sr.repo_id AND r.status = 'active'
		WHERE vs.version_id = $1 AND vs.status = 'active'
		ORDER BY r.id, sr.role, sr.id
		LIMIT $2
	`, versionID, handoffReadRowLimit)
	if err != nil {
		return nil, fmt.Errorf("list handoff SUT repositories: %w", err)
	}
	for rows.Next() {
		var repository versionapp.HandoffRepository
		if err := rows.Scan(&repository.ID, &repository.Name, &repository.Branch, &repository.Role); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan handoff SUT repository: %w", err)
		}
		repository.Source = "sut"
		repositories[repository.ID] = repository
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("scan handoff SUT repositories: %w", err)
	}
	rows.Close()

	overrideRows, err := r.pool.Query(ctx, `
		SELECT r.id, r.name, r.branch, vo.included
		FROM version_repository_override vo
		JOIN code_repository r ON r.id = vo.repo_id AND r.status = 'active'
		WHERE vo.version_id = $1
		ORDER BY r.id
		LIMIT $2
	`, versionID, handoffReadRowLimit)
	if err != nil {
		return nil, fmt.Errorf("list handoff repository overrides: %w", err)
	}
	defer overrideRows.Close()
	for overrideRows.Next() {
		var repository versionapp.HandoffRepository
		var included bool
		if err := overrideRows.Scan(&repository.ID, &repository.Name, &repository.Branch, &included); err != nil {
			return nil, fmt.Errorf("scan handoff repository override: %w", err)
		}
		if !included {
			delete(repositories, repository.ID)
			continue
		}
		// The override table has no role; keep an inherited role when the
		// repository is already enabled for the SUT.
		if inherited, ok := repositories[repository.ID]; ok {
			repository.Role = inherited.Role
		}
		repository.Source = "override"
		repositories[repository.ID] = repository
	}
	if err := overrideRows.Err(); err != nil {
		return nil, fmt.Errorf("scan handoff repository overrides: %w", err)
	}

	result := make([]versionapp.HandoffRepository, 0, len(repositories))
	for _, repository := range repositories {
		result = append(result, repository)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if len(result) > handoffReadRowLimit {
		result = result[:handoffReadRowLimit]
	}
	return result, nil
}

func handoffPlanItemTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "completed", "cancelled", "canceled", "skipped":
		return true
	default:
		return false
	}
}

func handoffIDSet(ids []int64) map[int64]struct{} {
	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			set[id] = struct{}{}
		}
	}
	return set
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
	if !strings.HasPrefix(lower, "requirement:") {
		return 0, false
	}
	raw := strings.TrimSpace(trimmed[len("requirement:"):])
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
	for _, kind := range []string{
		agenttools.ClarificationSearchKindDecision,
		agenttools.ClarificationSearchKindConstraint,
	} {
		if _, ok := kinds[kind]; ok {
			factKinds = append(factKinds, kind)
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
		WHERE h.version_id = $1 AND h.kind = 'clarification_question'
		  AND ($2 = '' OR h.question::text ILIKE '%' || $2 || '%'
		       OR h.response::text ILIKE '%' || $2 || '%')
		  AND ($3 = 0
		       OR strpos(lower(h.question::text), 'requirement:' || $3::text) > 0
		       OR ($4::text <> '' AND strpos(lower(h.question::text),
		            lower('requirement:' || $4::text)) > 0))
		ORDER BY h.id DESC
		LIMIT $5
	`, query.VersionID, query.Query, query.RequirementID, externalRequirementID, clarificationSearchRowLimit)
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
			  AND (v.source_ref = 'requirement:' || ($4::bigint)::text
			       OR v.payload->>'requirement_id' = ($4::bigint)::text
			       OR v.payload->>'requirement' = ($4::bigint)::text
			       OR ($5::text <> '' AND (
			            v.source_ref = 'requirement:' || $5::text
			            OR v.payload->>'requirement_id' = $5::text
			            OR v.payload->>'requirement' = $5::text)))
			ORDER BY v.id DESC
			LIMIT $6
		`
		args = append(args, query.VersionID, kinds, query.Query, query.RequirementID,
			externalRequirementID, clarificationSearchRowLimit)
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
		  AND c.kind = 'clarification'
		  AND ($2 = '' OR m.parts::text ILIKE '%' || $2 || '%')
		  AND ($3 = 0 OR EXISTS (
		      SELECT 1
		      FROM jsonb_array_elements(m.refs) AS ref
		      WHERE ref->>'kind' = 'requirement'
		        AND (ref->>'id' = $3::text
		             OR ($4::text <> '' AND ref->>'id' = $4::text))
		  ))
		ORDER BY m.id DESC
		LIMIT $5
	`, query.VersionID, query.Query, query.RequirementID, externalRequirementID, clarificationSearchRowLimit)
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
		if part.Type != "text" {
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
		if !strings.EqualFold(strings.TrimSpace(ref.Kind), "requirement") {
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
