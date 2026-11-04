package versionadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Briefing reader limits. The briefing renders at most five plan/fact/knowledge
// entries and the per-section token budgets truncate the rest; the read limits
// keep a large version from loading an unbounded amount of data every round
// while still giving the deterministic ranking enough candidates.
const (
	briefingRequirementLimit      = 20
	briefingPlanLimit             = 20
	briefingFactLimit             = 20
	briefingKnowledgeLimit        = 20
	briefingKnowledgeCandidates   = 100
	briefingOpenItemLimit         = 20
	briefingFactSummaryRunes      = 240
	briefingKnowledgeSummaryRunes = 240
	briefingOpenItemLabelRunes    = 240
)

// briefingSource is the narrow read seam behind the production briefing
// reader. The SQL implementation issues read-only queries for one version;
// tests inject an in-memory source to pin the empty/ordering/limit behaviour.
type briefingSource interface {
	Load(ctx context.Context, versionID int64) (agentcontext.Source, error)
}

// BriefingReader implements agentcontext.Reader for the derived version
// briefing (spec §6.2). It is read-only, never uses a clock or randomness, and
// bounds every collection before returning it, so one large version cannot
// grow the prompt without limit.
type BriefingReader struct {
	source briefingSource
}

var _ agentcontext.Reader = (*BriefingReader)(nil)

// NewBriefingReader wires the production reader over the PostgreSQL pool. The
// reader never writes.
func NewBriefingReader(pool *pgxpool.Pool) (*BriefingReader, error) {
	if pool == nil {
		return nil, fmt.Errorf("versionadapter: briefing reader requires a database pool")
	}
	return &BriefingReader{source: &sqlBriefingSource{pool: pool}}, nil
}

// newBriefingReader wires a custom source. It is used by the focused unit tests
// and keeps the SQL dependency out of the reader contract.
func newBriefingReader(source briefingSource) *BriefingReader {
	return &BriefingReader{source: source}
}

// Load returns the briefing source of one version. It validates the version ID
// and normalizes the source before returning it: every slice is non-nil (empty
// versions produce zero/empty sections) and every collection is bounded.
func (r *BriefingReader) Load(ctx context.Context, versionID int64) (agentcontext.Source, error) {
	if versionID <= 0 {
		return agentcontext.Source{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if r == nil || r.source == nil {
		return agentcontext.Source{}, errors.New("versionadapter: briefing source is not configured")
	}
	source, err := r.source.Load(ctx, versionID)
	if err != nil {
		return agentcontext.Source{}, fmt.Errorf("load version briefing source: %w", err)
	}
	return normalizeBriefingSource(source), nil
}

// sqlBriefingSource reads every briefing input from PostgreSQL. All reads of
// one Load share a read-only repeatable-read transaction, so a concurrent write
// cannot produce a briefing mixed from two snapshots.
type sqlBriefingSource struct {
	pool *pgxpool.Pool
}

// Load implements briefingSource.
func (s *sqlBriefingSource) Load(ctx context.Context, versionID int64) (agentcontext.Source, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return agentcontext.Source{}, fmt.Errorf("begin briefing read transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var source agentcontext.Source
	if err := loadBriefingVersion(ctx, tx, versionID, &source); err != nil {
		return agentcontext.Source{}, err
	}
	if err := loadBriefingRequirements(ctx, tx, versionID, &source); err != nil {
		return agentcontext.Source{}, err
	}
	if err := loadBriefingCases(ctx, tx, versionID, &source); err != nil {
		return agentcontext.Source{}, err
	}
	if err := loadBriefingPlan(ctx, tx, versionID, &source); err != nil {
		return agentcontext.Source{}, err
	}
	if err := loadBriefingFacts(ctx, tx, versionID, &source); err != nil {
		return agentcontext.Source{}, err
	}
	if err := loadBriefingExecution(ctx, tx, versionID, &source); err != nil {
		return agentcontext.Source{}, err
	}
	if err := loadBriefingOpenQuestions(ctx, tx, versionID, &source); err != nil {
		return agentcontext.Source{}, err
	}
	if err := loadBriefingKnowledge(ctx, tx, versionID, &source); err != nil {
		return agentcontext.Source{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agentcontext.Source{}, fmt.Errorf("commit briefing read transaction: %w", err)
	}
	return source, nil
}

func loadBriefingVersion(ctx context.Context, tx pgx.Tx, versionID int64, source *agentcontext.Source) error {
	err := tx.QueryRow(ctx, `
		SELECT title, stage, COALESCE(blocker, '')
		FROM version
		WHERE id = $1
	`, versionID).Scan(&source.Version.Goal, &source.Version.Stage, &source.Version.Blocker)
	if errors.Is(err, pgx.ErrNoRows) {
		return versiondomain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read briefing version: %w", err)
	}
	return nil
}

func loadBriefingRequirements(ctx context.Context, tx pgx.Tx, versionID int64, source *agentcontext.Source) error {
	// Closed requirements were confirmed before they closed, so they stay in
	// the confirmed baseline; only draft requirements are open clarification
	// gaps.
	rows, err := tx.Query(ctx, `
		SELECT rv.title, rv.acceptance_criteria
		FROM requirement r
		JOIN requirement_version rv ON rv.id = r.current_version_id
		WHERE r.version_id = $1
		  AND r.status IN ('clarified', 'closed')
		ORDER BY r.id
		LIMIT $2
	`, versionID, briefingRequirementLimit)
	if err != nil {
		return fmt.Errorf("read briefing confirmed requirements: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var requirement agentcontext.RequirementSnapshot
		if err := rows.Scan(&requirement.Title, &requirement.Acceptance); err != nil {
			return fmt.Errorf("scan briefing confirmed requirement: %w", err)
		}
		source.Requirements.Confirmed = append(source.Requirements.Confirmed, requirement)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read briefing confirmed requirements: %w", err)
	}

	rows, err = tx.Query(ctx, `
		SELECT rv.title
		FROM requirement r
		JOIN requirement_version rv ON rv.id = r.current_version_id
		WHERE r.version_id = $1
		  AND r.status = 'draft'
		ORDER BY r.id
		LIMIT $2
	`, versionID, briefingRequirementLimit)
	if err != nil {
		return fmt.Errorf("read briefing requirement gaps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return fmt.Errorf("scan briefing requirement gap: %w", err)
		}
		source.Requirements.Gaps = append(source.Requirements.Gaps, title)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read briefing requirement gaps: %w", err)
	}
	return nil
}

func loadBriefingCases(ctx context.Context, tx pgx.Tx, versionID int64, source *agentcontext.Source) error {
	rows, err := tx.Query(ctx, `
		SELECT cv.status, count(*)
		FROM case_entity e
		JOIN case_scenario s ON s.id = e.scenario_id
		JOIN case_version cv ON cv.id = e.current_version_id
		WHERE s.version_id = $1
		GROUP BY cv.status
		ORDER BY cv.status
	`, versionID)
	if err != nil {
		return fmt.Errorf("read briefing case counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return fmt.Errorf("scan briefing case count: %w", err)
		}
		switch status {
		case "draft":
			source.Cases.Draft = count
		case "confirmed":
			source.Cases.Confirmed = count
		case "stale":
			source.Cases.Stale = count
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read briefing case counts: %w", err)
	}

	rows, err = tx.Query(ctx, `
		SELECT rv.title
		FROM requirement r
		JOIN requirement_version rv ON rv.id = r.current_version_id
		WHERE r.version_id = $1
		  AND r.status = 'clarified'
		  AND NOT EXISTS (
			SELECT 1
			FROM case_scenario s
			JOIN case_entity e ON e.scenario_id = s.id
			JOIN case_version cv ON cv.id = e.current_version_id
			WHERE s.version_id = $1 AND s.requirement_id = r.id
		  )
		ORDER BY r.id
		LIMIT $2
	`, versionID, briefingRequirementLimit)
	if err != nil {
		return fmt.Errorf("read briefing coverage gaps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return fmt.Errorf("scan briefing coverage gap: %w", err)
		}
		source.Cases.CoverageGaps = append(source.Cases.CoverageGaps, title)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read briefing coverage gaps: %w", err)
	}
	return nil
}

func loadBriefingPlan(ctx context.Context, tx pgx.Tx, versionID int64, source *agentcontext.Source) error {
	rows, err := tx.Query(ctx, `
		SELECT id, title, status, depends_on, revision
		FROM version_plan_item
		WHERE version_id = $1
		  AND status NOT IN ('done', 'completed', 'cancelled', 'skipped', 'failed', 'closed')
		ORDER BY sort_order, id
		LIMIT $2
	`, versionID, briefingPlanLimit)
	if err != nil {
		return fmt.Errorf("read briefing plan: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item agentcontext.PlanItemSnapshot
		var dependsOn []int64
		if err := rows.Scan(&item.ID, &item.Title, &item.Status, &dependsOn, &item.Revision); err != nil {
			return fmt.Errorf("scan briefing plan item: %w", err)
		}
		item.DependsOn = make([]string, 0, len(dependsOn))
		for _, dependencyID := range dependsOn {
			item.DependsOn = append(item.DependsOn, strconv.FormatInt(dependencyID, 10))
		}
		source.Plan = append(source.Plan, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read briefing plan: %w", err)
	}
	return nil
}

func loadBriefingFacts(ctx context.Context, tx pgx.Tx, versionID int64, source *agentcontext.Source) error {
	// The candidate order mirrors the briefing renderer's fact ranking
	// (confidence first, missing confidence last, then newest), so the top
	// findings survive the read limit on a version with many facts.
	rows, err := tx.Query(ctx, `
		SELECT id, kind, key, payload, confidence, created_at
		FROM version_fact
		WHERE version_id = $1 AND superseded_by IS NULL
		ORDER BY confidence DESC NULLS LAST, created_at DESC, id DESC
		LIMIT $2
	`, versionID, briefingFactLimit)
	if err != nil {
		return fmt.Errorf("read briefing facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var fact agentcontext.FactSnapshot
		var payload []byte
		if err := rows.Scan(&fact.ID, &fact.Kind, &fact.Key, &payload, &fact.Confidence, &fact.CreatedAt); err != nil {
			return fmt.Errorf("scan briefing fact: %w", err)
		}
		fact.Summary = briefingFactSummary(payload)
		source.Findings = append(source.Findings, fact)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read briefing facts: %w", err)
	}
	return nil
}

func loadBriefingExecution(ctx context.Context, tx pgx.Tx, versionID int64, source *agentcontext.Source) error {
	var waveID int64
	// pause_reason survives the resume/finish transitions (the wave repository
	// keeps it when a transition passes an empty reason), so only a paused wave
	// may contribute a blocker. A running/done/cancelled wave must never surface
	// the previous pause reason.
	err := tx.QueryRow(ctx, `
		SELECT id, status,
			CASE WHEN status = 'paused' THEN COALESCE(pause_reason, '') ELSE '' END
		FROM run_wave
		WHERE version_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, versionID).Scan(&waveID, &source.Execution.WaveStatus, &source.Execution.Blocker)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read briefing latest wave: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT status, count(*)
		FROM case_run
		WHERE wave_id = $1
		GROUP BY status
		ORDER BY status
	`, waveID)
	if err != nil {
		return fmt.Errorf("read briefing wave case runs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return fmt.Errorf("scan briefing wave case run: %w", err)
		}
		switch status {
		case "passed":
			source.Execution.Passed = count
		case "failed":
			source.Execution.Failed = count
		case "blocked":
			source.Execution.Blocked = count
		case "skipped":
			source.Execution.Skipped = count
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read briefing wave case runs: %w", err)
	}
	return nil
}

func loadBriefingOpenQuestions(ctx context.Context, tx pgx.Tx, versionID int64, source *agentcontext.Source) error {
	rows, err := tx.Query(ctx, `
		SELECT kind, COALESCE(payload ->> 'reason', '')
		FROM human_request
		WHERE version_id = $1 AND status = 'pending'
		ORDER BY created_at, id
		LIMIT $2
	`, versionID, briefingOpenItemLimit)
	if err != nil {
		return fmt.Errorf("read briefing open human requests: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, reason string
		if err := rows.Scan(&kind, &reason); err != nil {
			return fmt.Errorf("scan briefing open human request: %w", err)
		}
		label := oneLineBriefingText(reason)
		if label == "" {
			label = kind
		}
		if kind == "login_takeover" {
			source.OpenQuestions.HumanRequests = append(source.OpenQuestions.HumanRequests, label)
		} else {
			source.OpenQuestions.Questions = append(source.OpenQuestions.Questions, label)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read briefing open human requests: %w", err)
	}
	return nil
}

// briefingKnowledgeScope is the binding/scope projection of one version used to
// decide which approved knowledge items are visible to it.
type briefingKnowledgeScope struct {
	families      map[string]struct{}
	systemIDs     map[int64]struct{}
	environmentID map[int64]struct{}
	pageKeys      map[string]struct{}
	apiKeys       map[string]struct{}
	accountRoles  map[string]struct{}
}

func loadBriefingKnowledge(ctx context.Context, tx pgx.Tx, versionID int64, source *agentcontext.Source) error {
	scope, err := readBriefingKnowledgeScope(ctx, tx, versionID)
	if err != nil {
		return err
	}

	// Binding/scope filtering happens in Go, so the candidate pool is bounded to
	// the top approved items by confidence. This is a deterministic bounded
	// top-K trade-off: a version with more than briefingKnowledgeCandidates
	// higher-confidence items that are invisible to it could miss a visible
	// lower-confidence item that would still make the rendered top-K.
	//
	// The status and the source-version existence are read with the item, not
	// assumed: the agent's briefing builder re-applies the injection policy
	// (approved only, never a source-gone item) and can only do so when the read
	// model carries them. The existence probe is part of the same repeatable-read
	// snapshot as the item read, and matches the Inject/Retrieve policy: a nil
	// source version is not stale, and a source read error is fail-open (here it
	// can only fail the whole briefing read, never silently mark an item stale).
	rows, err := tx.Query(ctx, `
		SELECT ki.id, ki.scope, ki.status, COALESCE(ki.system_key, ''),
			ki.title, ki.content, ki.confidence, ki.bindings,
			(ki.source_version_id IS NOT NULL AND NOT EXISTS (
				SELECT 1 FROM version v WHERE v.id = ki.source_version_id
			)) AS source_stale
		FROM knowledge_item ki
		WHERE ki.status = 'approved'
		ORDER BY ki.confidence DESC NULLS LAST, ki.id DESC
		LIMIT $1
	`, briefingKnowledgeCandidates)
	if err != nil {
		return fmt.Errorf("read briefing knowledge: %w", err)
	}
	defer rows.Close()

	items := make([]agentcontext.KnowledgeSnapshot, 0, briefingKnowledgeLimit)
	for rows.Next() {
		var item agentcontext.KnowledgeSnapshot
		var itemScope, systemKey string
		var content string
		var bindingsRaw []byte
		if err := rows.Scan(&item.ID, &itemScope, &item.Status, &systemKey, &item.Title, &content,
			&item.Confidence, &bindingsRaw, &item.SourceStale); err != nil {
			return fmt.Errorf("scan briefing knowledge: %w", err)
		}
		var bindings knowledge.Bindings
		if len(bindingsRaw) > 0 {
			if err := json.Unmarshal(bindingsRaw, &bindings); err != nil {
				// A malformed binding row is not visible to any version; skip it
				// instead of failing the whole briefing.
				continue
			}
		}
		if !scope.visible(itemScope, systemKey, bindings) {
			continue
		}
		item.Summary = truncateBriefingText(oneLineBriefingText(content), briefingKnowledgeSummaryRunes)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read briefing knowledge: %w", err)
	}

	sort.Slice(items, func(left, right int) bool {
		if compared := compareBriefingConfidence(items[left].Confidence, items[right].Confidence); compared != 0 {
			return compared < 0
		}
		if items[left].Title != items[right].Title {
			return items[left].Title < items[right].Title
		}
		if items[left].Summary != items[right].Summary {
			return items[left].Summary < items[right].Summary
		}
		return items[left].ID < items[right].ID
	})
	if len(items) > briefingKnowledgeLimit {
		items = items[:briefingKnowledgeLimit]
	}
	source.Knowledge = items
	return nil
}

func readBriefingKnowledgeScope(ctx context.Context, tx pgx.Tx, versionID int64) (briefingKnowledgeScope, error) {
	scope := briefingKnowledgeScope{
		families:      map[string]struct{}{},
		systemIDs:     map[int64]struct{}{},
		environmentID: map[int64]struct{}{},
		pageKeys:      map[string]struct{}{},
		apiKeys:       map[string]struct{}{},
		accountRoles:  map[string]struct{}{},
	}

	rows, err := tx.Query(ctx, `
		SELECT DISTINCT s.family
		FROM version_sut vs
		JOIN sut s ON s.id = vs.sut_id
		WHERE vs.version_id = $1 AND s.family IS NOT NULL AND btrim(s.family) <> ''
		ORDER BY s.family
	`, versionID)
	if err != nil {
		return scope, fmt.Errorf("read briefing knowledge families: %w", err)
	}
	for rows.Next() {
		var family string
		if err := rows.Scan(&family); err != nil {
			rows.Close()
			return scope, fmt.Errorf("scan briefing knowledge family: %w", err)
		}
		scope.families[family] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return scope, fmt.Errorf("read briefing knowledge families: %w", err)
	}

	pageIDs := make([]int64, 0, 8)
	apiIDs := make([]int64, 0, 8)
	rows, err = tx.Query(ctx, `
		SELECT sut_id, environment_id, scope
		FROM version_sut
		WHERE version_id = $1
		ORDER BY id
	`, versionID)
	if err != nil {
		return scope, fmt.Errorf("read briefing version bindings: %w", err)
	}
	for rows.Next() {
		var sutID, environmentID int64
		var scopeRaw []byte
		if err := rows.Scan(&sutID, &environmentID, &scopeRaw); err != nil {
			rows.Close()
			return scope, fmt.Errorf("scan briefing version binding: %w", err)
		}
		scope.systemIDs[sutID] = struct{}{}
		scope.environmentID[environmentID] = struct{}{}
		var binding struct {
			Pages []int64 `json:"pages"`
			APIs  []int64 `json:"apis"`
		}
		if len(scopeRaw) > 0 && json.Unmarshal(scopeRaw, &binding) == nil {
			pageIDs = append(pageIDs, binding.Pages...)
			apiIDs = append(apiIDs, binding.APIs...)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return scope, fmt.Errorf("read briefing version bindings: %w", err)
	}

	if len(pageIDs) > 0 {
		rows, err = tx.Query(ctx, `
			SELECT route_template
			FROM sut_page
			WHERE id = ANY($1::bigint[])
			ORDER BY id
		`, pageIDs)
		if err != nil {
			return scope, fmt.Errorf("read briefing page keys: %w", err)
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return scope, fmt.Errorf("scan briefing page key: %w", err)
			}
			scope.pageKeys[key] = struct{}{}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return scope, fmt.Errorf("read briefing page keys: %w", err)
		}
	}

	if len(apiIDs) > 0 {
		rows, err = tx.Query(ctx, `
			SELECT method || ' ' || path_template
			FROM sut_api
			WHERE id = ANY($1::bigint[])
			ORDER BY id
		`, apiIDs)
		if err != nil {
			return scope, fmt.Errorf("read briefing API keys: %w", err)
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return scope, fmt.Errorf("scan briefing API key: %w", err)
			}
			scope.apiKeys[key] = struct{}{}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return scope, fmt.Errorf("read briefing API keys: %w", err)
		}
	}

	rows, err = tx.Query(ctx, `
		SELECT vsr.role
		FROM version_sut_resource vsr
		JOIN version_sut vs ON vs.id = vsr.version_sut_id
		WHERE vs.version_id = $1 AND vsr.kind = 'account' AND vsr.selected AND vsr.role <> ''
		ORDER BY vsr.id
	`, versionID)
	if err != nil {
		return scope, fmt.Errorf("read briefing account roles: %w", err)
	}
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			rows.Close()
			return scope, fmt.Errorf("scan briefing account role: %w", err)
		}
		scope.accountRoles[role] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return scope, fmt.Errorf("read briefing account roles: %w", err)
	}
	return scope, nil
}

// visible reports whether one approved knowledge item is visible to the
// version: a system-scoped item must match one of the bound SUT families, and
// every binding dimension set on the item must match a version binding. The
// dimensions are matched independently against the union of the version
// bindings (not against a single version_sut row); an unset dimension is
// unconstrained.
func (s briefingKnowledgeScope) visible(scope, systemKey string, bindings knowledge.Bindings) bool {
	switch scope {
	case "global":
	case "system":
		if _, ok := s.families[systemKey]; !ok {
			return false
		}
	default:
		return false
	}
	if bindings.IsZero() {
		return true
	}
	if bindings.SystemID != nil {
		if _, ok := s.systemIDs[*bindings.SystemID]; !ok {
			return false
		}
	}
	if bindings.EnvironmentID != nil {
		if _, ok := s.environmentID[*bindings.EnvironmentID]; !ok {
			return false
		}
	}
	if bindings.PageKey != nil {
		if _, ok := s.pageKeys[*bindings.PageKey]; !ok {
			return false
		}
	}
	if bindings.APIKey != nil {
		if _, ok := s.apiKeys[*bindings.APIKey]; !ok {
			return false
		}
	}
	if bindings.AccountRole != nil {
		if _, ok := s.accountRoles[*bindings.AccountRole]; !ok {
			return false
		}
	}
	return true
}

// normalizeBriefingSource applies the reader bounds and the required empty
// semantics. The SQL source already orders and limits its rows; this pass makes
// the contract explicit for every source (including test fakes): no nil slices,
// dependency-first plan order, bounded collections, only a paused wave keeps a
// blocker, and open-question labels are bounded.
func normalizeBriefingSource(source agentcontext.Source) agentcontext.Source {
	source.Requirements.Confirmed = limitBriefingSlice(source.Requirements.Confirmed, briefingRequirementLimit)
	source.Requirements.Gaps = limitBriefingSlice(source.Requirements.Gaps, briefingRequirementLimit)
	source.Cases.CoverageGaps = limitBriefingSlice(source.Cases.CoverageGaps, briefingRequirementLimit)
	source.Plan = orderBriefingPlan(source.Plan)
	source.Plan = limitBriefingSlice(source.Plan, briefingPlanLimit)
	source.Findings = limitBriefingSlice(source.Findings, briefingFactLimit)
	source.Knowledge = limitBriefingSlice(source.Knowledge, briefingKnowledgeLimit)
	source.Execution = normalizeBriefingExecution(source.Execution)
	source.OpenQuestions.Questions = capBriefingLabels(
		limitBriefingSlice(source.OpenQuestions.Questions, briefingOpenItemLimit), briefingOpenItemLabelRunes)
	source.OpenQuestions.HumanRequests = capBriefingLabels(
		limitBriefingSlice(source.OpenQuestions.HumanRequests, briefingOpenItemLimit), briefingOpenItemLabelRunes)
	return source
}

// normalizeBriefingExecution enforces the ExecutionSnapshot contract: only a
// paused wave carries a blocker, and the internal waiting_human pause token is
// not user-visible text. A human pause keeps its user-visible reason in the open
// human request (rendered in the open-questions section), so the token is
// dropped instead of leaked into the prompt.
func normalizeBriefingExecution(in agentcontext.ExecutionSnapshot) agentcontext.ExecutionSnapshot {
	blocker := oneLineBriefingText(in.Blocker)
	if in.WaveStatus != string(execution.WavePaused) || blocker == execution.PauseReasonWaitingHuman {
		blocker = ""
	}
	in.Blocker = blocker
	return in
}

// capBriefingLabels bounds every open-question label. The open-questions section
// is protected from the renderer's truncation, so an unbounded human request
// reason could otherwise grow the briefing without limit.
func capBriefingLabels(labels []string, maxRunes int) []string {
	capped := make([]string, len(labels))
	for index, label := range labels {
		capped[index] = truncateBriefingText(label, maxRunes)
	}
	return capped
}

func limitBriefingSlice[T any](items []T, limit int) []T {
	if items == nil {
		return []T{}
	}
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

// orderBriefingPlan applies the port's dependency-first order. The source
// supplies items in (sort_order, id) order; a dependant that sorts before its
// dependency is moved after it, while independent items keep their order. A
// dependency reference is rewritten to the dependency title for rendering; a
// reference outside the open set keeps its decimal ID. Cycles are data
// corruption and fall back to the supplied order instead of hanging.
func orderBriefingPlan(items []agentcontext.PlanItemSnapshot) []agentcontext.PlanItemSnapshot {
	if len(items) < 2 {
		return items
	}
	ordered := append([]agentcontext.PlanItemSnapshot(nil), items...)
	byID := make(map[int64]int, len(ordered))
	indegree := make([]int, len(ordered))
	dependants := make([][]int, len(ordered))
	for index, item := range ordered {
		if item.ID > 0 {
			byID[item.ID] = index
		}
	}
	for index, item := range ordered {
		for _, reference := range item.DependsOn {
			dependencyID, err := strconv.ParseInt(strings.TrimSpace(reference), 10, 64)
			if err != nil || dependencyID <= 0 || dependencyID == item.ID {
				continue
			}
			dependencyIndex, ok := byID[dependencyID]
			if !ok {
				continue
			}
			indegree[index]++
			dependants[dependencyIndex] = append(dependants[dependencyIndex], index)
		}
	}

	emitted := make([]bool, len(ordered))
	result := make([]agentcontext.PlanItemSnapshot, 0, len(ordered))
	for len(result) < len(ordered) {
		progressed := false
		for index := range ordered {
			if emitted[index] || indegree[index] > 0 {
				continue
			}
			emitted[index] = true
			result = append(result, ordered[index])
			for _, dependant := range dependants[index] {
				indegree[dependant]--
			}
			progressed = true
			break
		}
		if !progressed {
			// Cycle fallback: keep every row in its supplied order.
			for index := range ordered {
				if !emitted[index] {
					emitted[index] = true
					result = append(result, ordered[index])
				}
			}
		}
	}

	titles := make(map[int64]string, len(result))
	for _, item := range result {
		if item.ID > 0 {
			titles[item.ID] = oneLineBriefingText(item.Title)
		}
	}
	for index := range result {
		if len(result[index].DependsOn) == 0 {
			result[index].DependsOn = []string{}
			continue
		}
		references := make([]string, 0, len(result[index].DependsOn))
		for _, reference := range result[index].DependsOn {
			dependencyID, err := strconv.ParseInt(strings.TrimSpace(reference), 10, 64)
			if err == nil {
				if title := titles[dependencyID]; title != "" {
					references = append(references, title)
					continue
				}
			}
			references = append(references, reference)
		}
		result[index].DependsOn = references
	}
	return result
}

func briefingFactSummary(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil || len(object) == 0 {
		return ""
	}
	for _, key := range []string{"summary", "title", "message", "reason", "description"} {
		if value, ok := object[key].(string); ok {
			if line := oneLineBriefingText(value); line != "" {
				return truncateBriefingText(line, briefingFactSummaryRunes)
			}
		}
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return ""
	}
	return truncateBriefingText(string(encoded), briefingFactSummaryRunes)
}

func oneLineBriefingText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncateBriefingText(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

// compareBriefingConfidence ranks higher confidence first and missing
// confidence last, matching the briefing renderer's knowledge order.
func compareBriefingConfidence(left, right *float64) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return 1
	case right == nil:
		return -1
	case *left > *right:
		return -1
	case *left < *right:
		return 1
	default:
		return 0
	}
}
