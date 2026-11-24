package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// judgeExhaustedFactKind is the version_fact kind of the judge-unavailable
// escalation fact (T6 JudgeExhaustedStore port). The fact lives in the version
// context's append-only version_fact mechanism; the design context never creates
// a second fact table and never imports the version context.
const judgeExhaustedFactKind = "judge_exhausted"

// judgeModelSettingKey is the registered settings key naming the design_judge
// LLM model (settings catalog, T1). Empty inherits the platform default model.
const judgeModelSettingKey = "design.judge_model"

// judgeExhaustedFactKey is the version_fact key binding an escalation to exactly
// one case version. HasJudgeExhausted matches this key exactly and only current
// facts (superseded_by IS NULL), so an older version's escalation is never this
// version's. The shape is the composition root's choice (T6 handoff §5).
func judgeExhaustedFactKey(caseID, caseVersionID int64) string {
	return fmt.Sprintf("case:%d:case_version:%d", caseID, caseVersionID)
}

// judgeExhaustedFactStore adapts the version FactService to the design context's
// neutral JudgeExhaustedStore port (T13 wiring of the T6 handoff). Insert rides
// FactService.RecordFact so the version context stays the single authority for
// fact ID/content-hash/supersede rules (no hand-written SQL replicating them) and
// so the append's own dedupe outcome tells the caller whether this is a NEW
// escalation. Has is a read-only current-fact existence check by kind+key (the
// same read-only SQL source pattern as the version briefing reader); it never
// writes.
type judgeExhaustedFactStore struct {
	facts *versionapp.FactService
	pool  *pgxpool.Pool
}

var _ designapp.JudgeExhaustedStore = (*judgeExhaustedFactStore)(nil)

// InsertJudgeExhausted appends the escalation fact and reports whether it was
// newly created. AlreadyRecorded is the store's own dedupe signal (the unique
// (version, kind, key, content_hash) index returned an existing row), so the
// transition is decided by the database, not by a read-then-write race.
func (s *judgeExhaustedFactStore) InsertJudgeExhausted(ctx context.Context, versionID, requirementID, caseID, caseVersionID int64) (bool, error) {
	if s.facts == nil {
		return false, designapp.ErrJudgeExhaustedStoreUnavailable
	}
	outcome, err := s.facts.RecordFact(ctx, versiondomain.Fact{
		VersionID: versionID,
		Kind:      judgeExhaustedFactKind,
		Key:       judgeExhaustedFactKey(caseID, caseVersionID),
		Payload: map[string]any{
			"requirement_id":  platformid.Format(requirementID),
			"case_id":         platformid.Format(caseID),
			"case_version_id": platformid.Format(caseVersionID),
		},
		Source: "design_judge",
	})
	if err != nil {
		return false, err
	}
	return !outcome.AlreadyRecorded, nil
}

func (s *judgeExhaustedFactStore) HasJudgeExhausted(ctx context.Context, caseID, caseVersionID int64) (bool, error) {
	if s.pool == nil {
		return false, designapp.ErrJudgeExhaustedStoreUnavailable
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM version_fact
			WHERE kind = $1 AND key = $2 AND superseded_by IS NULL
		)
	`, judgeExhaustedFactKind, judgeExhaustedFactKey(caseID, caseVersionID)).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("read judge_exhausted fact: %w", err)
	}
	return exists, nil
}

// designJudgeRepo is the design repository surface the judgment service and the
// shared review gate read (*designpg.Repository satisfies it): the current case
// version bound to a judgment, and the version graph the gate enumerates.
type designJudgeRepo interface {
	designapp.JudgmentCaseReader
	designapp.ReviewGraphReader
}

// designJudgeComponents bundles the judge wiring the composition root installs
// onto the design graph (confirmation gate), the design planner (judge scheduler
// + converge review gate) and the version stage service (manual review gate).
type designJudgeComponents struct {
	Store      *designpg.JudgmentStore
	Service    *designapp.JudgmentService
	Gate       designapp.JudgmentGate
	ReviewGate *designapp.ReviewGateService
	Exhausted  designapp.JudgeExhaustedStore
}

// buildDesignJudge constructs the T6 judgment service/repository, the T7
// confirmation gate, the T9 shared review gate and the judge-exhausted fact
// adapter. ONE ReviewGateService instance is returned so the composition root
// wires the SAME predicate into both advance edges (planner converge + version
// manual advance): one predicate, one truth (T9).
func buildDesignJudge(
	pool *pgxpool.Pool,
	ids platformid.Generator,
	clk clock.Clock,
	events eventingapp.Publisher,
	settings designapp.SettingsReader,
	repo designJudgeRepo,
	facts *versionapp.FactService,
) designJudgeComponents {
	exhausted := &judgeExhaustedFactStore{facts: facts, pool: pool}
	store := designpg.NewJudgmentStore(pool, exhausted)
	service := designapp.NewJudgmentService(store, repo, ids, clk, events, settings)
	return designJudgeComponents{
		Store:      store,
		Service:    service,
		Gate:       designapp.NewJudgmentGate(service, store),
		ReviewGate: designapp.NewReviewGateService(repo, service),
		Exhausted:  exhausted,
	}
}

// judgeModelSettingReader reads the design.judge_model setting (*settingsapp.Service).
type judgeModelSettingReader interface {
	String(ctx context.Context, key string) (string, error)
}

// judgeProfileModelWriter writes the design_judge profile model (*agentpg.RuntimeRepository).
type judgeProfileModelWriter interface {
	UpdateProfile(ctx context.Context, key string, update agent.ProfileUpdate) (agent.AgentProfile, bool, error)
}

// wireJudgeModel reconciles the design_judge profile model to the
// design.judge_model setting at assembly (T13). A non-empty value must name a
// configured LLM model (validated against the default configuration's catalog)
// and is written onto the stored profile; an empty value clears the model to nil
// so the run inherits the platform default. The setting is the SINGLE authority
// for the judge model (constitution V): ProfileAdminService.Patch rejects a
// Model PATCH on design_judge with a field error pointing at this setting, so
// there is no competing admin write path. EnsureProfiles still never overwrites
// the prompt/tools/budgets.
func wireJudgeModel(ctx context.Context, settings judgeModelSettingReader, models agentapp.ModelCatalog, repo judgeProfileModelWriter) error {
	raw, err := settings.String(ctx, judgeModelSettingKey)
	if err != nil {
		return fmt.Errorf("read %s: %w", judgeModelSettingKey, err)
	}
	model := strings.TrimSpace(raw)
	var next *string
	if model != "" {
		if models == nil {
			return fmt.Errorf("LLM model catalog is not configured to validate %s", judgeModelSettingKey)
		}
		if _, err := models.ResolveModel(ctx, 0, model); err != nil {
			return fmt.Errorf("%s %q is not a model of the default LLM configuration: %w", judgeModelSettingKey, model, err)
		}
		next = &model
	}
	if _, _, err := repo.UpdateProfile(ctx, agentapp.ProfileKeyDesignJudge, agent.ProfileUpdate{SetModel: true, Model: next}); err != nil {
		return fmt.Errorf("wire design_judge model: %w", err)
	}
	return nil
}

// judgeGenerationWorkItemSource is the narrow, TARGETED row source the judge
// generation counters need: every design.judge work item of one requirement
// version, regardless of status or draft-set anchor. The production
// implementation (judgeGenerationSQLSource) queries work_item by
// version/type/requirement_version_id directly — never the capped generic
// ListByVersion window, where a busy version's newer rows push the older judge
// generations out of the newest-500 page. An undercounted sequence derives a
// dedupe key that collides with a terminal generation: Ensure then reports
// created=false with no error and the draft set is stranded pending forever
// (spec SC-E4 "无永久 pending"). Same hazard and same fix as
// judgeReworkWorkItemSource.
type judgeGenerationWorkItemSource interface {
	JudgeItemsForRequirementVersion(ctx context.Context, versionID, requirementVersionID int64) ([]agent.WorkItem, error)
}

// judgeGenerationSQLSource reads the design.judge rows of one requirement version
// directly from work_item (read-only, the same composition-root SQL-source
// pattern as judgeReworkSQLSource). The (version_id, type) prefix rides the
// uk_work_item_dedupe index; payload->>'requirement_version_id' normalizes JSON
// string and number IDs to the same decimal text. Judge items are 0.2.1 TYPE-path
// rows (kind is empty); the kind guard keeps the WorkTypeForItem convention
// without giving up the index prefix. NO LIMIT window and NO draft-set filter:
// the dedupe SEQUENCE counts every generation of the requirement version, while
// the BUDGET is matched against the stored anchor in Go
// (judgeGenerationDedupeKey).
type judgeGenerationSQLSource struct {
	pool *pgxpool.Pool
}

var _ judgeGenerationWorkItemSource = (*judgeGenerationSQLSource)(nil)

func (s *judgeGenerationSQLSource) JudgeItemsForRequirementVersion(ctx context.Context, versionID, requirementVersionID int64) ([]agent.WorkItem, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("judge generation SQL source is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, type, kind, status, attempts, max_attempts, payload, created_at
		FROM work_item
		WHERE version_id = $1 AND type = $2 AND (kind = '' OR kind = $2)
		  AND payload->>'requirement_version_id' = $3
		ORDER BY created_at DESC, id DESC
	`, versionID, designapp.TaskTypeDesignJudge, platformid.Format(requirementVersionID))
	if err != nil {
		return nil, fmt.Errorf("read design judge work items: %w", err)
	}
	defer rows.Close()
	items := make([]agent.WorkItem, 0)
	for rows.Next() {
		var item agent.WorkItem
		if err := rows.Scan(&item.ID, &item.Type, &item.Kind, &item.Status,
			&item.Attempts, &item.MaxAttempts, &item.Payload, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan design judge work item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read design judge work items: %w", err)
	}
	return items, nil
}

// judgeReworkWorkItemSource is the narrow, TARGETED row source the rework
// provenance reader needs: every design.rework work item of one logical case,
// regardless of status. The production implementation (judgeReworkSQLSource)
// queries work_item by version/type/case directly — never the capped generic
// ListByVersion window, where a long version's chat turns could push the
// rework row out of the newest-500 page and fake a "no rework" answer.
type judgeReworkWorkItemSource interface {
	ReworkItemsForCase(ctx context.Context, versionID, caseID int64) ([]agent.WorkItem, error)
}

// judgeReworkWorkItemReader derives, from the design.rework work items
// themselves, when the rework responding to ONE SPECIFIC FAIL was requested
// (v0.2.4-E T14 fix round 1, SC-E4 round provenance). The design context's own
// CountReworks (max(round)-1 over design_judgment) cannot tell a judge-triggered
// rework apart from a Host edit: case_version.trigger only records
// normal/discovery_delta/rollback, never "rework". The composition root CAN see
// the design.rework work_item rows (agent context), and their payload already
// carries the exact provenance binding: handleJudgeFail sets case_version_id to
// the FAILED VERSION BEING REWORKED FROM (dedupe rework:<case>:<failedVersion>).
// Matching that from-version identifies the rework responding to the latest
// fail — STATUS IRRELEVANT: the request exists at creation, and the judge is
// routinely enqueued (reconciler → ensureCriticTasks → ensureJudgeTasks) while
// the rework item is still open/running. Filtering on terminal status let the
// judge settle before the rework item finished, keep round 0, and silently
// bypass the rework cap (review Important, fix round 1).
type judgeReworkWorkItemReader struct {
	source judgeReworkWorkItemSource
}

// ReworkRequestedAt returns the CreatedAt of the most recent design.rework work
// item whose payload is bound to (caseID, caseVersionID) — the failed version
// the rework responds to — or the zero time if none exists. Status is never
// filtered. A nil source fails closed (never silently reports "no rework").
func (r *judgeReworkWorkItemReader) ReworkRequestedAt(ctx context.Context, versionID, caseID, caseVersionID int64) (time.Time, error) {
	if r == nil || r.source == nil {
		return time.Time{}, fmt.Errorf("judge rework provenance reader is not configured")
	}
	items, err := r.source.ReworkItemsForCase(ctx, versionID, caseID)
	if err != nil {
		return time.Time{}, err
	}
	var latest time.Time
	for _, item := range items {
		if agentapp.WorkTypeForItem(item) != designapp.TaskTypeDesignRework {
			continue
		}
		payload, err := agentapp.ParseWorkItemPayload(item.Payload)
		if err != nil || payload.CaseID != caseID || payload.CaseVersionID != caseVersionID {
			continue
		}
		if item.CreatedAt.After(latest) {
			latest = item.CreatedAt
		}
	}
	return latest, nil
}

// judgeReworkBudgetReader derives the ESCALATION rework budget of one logical
// case from the design.rework work items themselves: how many of them already
// consumed budget (spec SC-E4: the rework count derives from rework FACTS). The
// design context's CountReworks (max(round)-1 over design_judgment) is the
// ROUND-PROGRESSION counter and cannot see a rework that died: a dead rework is
// terminal but produces no revised draft and no new judgment round, so the
// round-derived count stays 0, the planner keeps asking for the same dedupe key
// (Ensure never revives a dead row) and the case strands in review_state
// reworking forever — the permanent pending SC-E4 forbids. The composition root
// CAN see the work_item rows, so the terminal count is read here and the design
// context consumes it through its neutral ReworkBudgetReader port.
type judgeReworkBudgetReader struct {
	source judgeReworkWorkItemSource
}

var _ designapp.ReworkBudgetReader = (*judgeReworkBudgetReader)(nil)

// TerminalReworkCount counts the case's design.rework work items that consumed
// rework budget. A nil source fails closed (never silently reports "no budget
// spent", which would grant unbounded rework rounds).
func (r *judgeReworkBudgetReader) TerminalReworkCount(ctx context.Context, versionID, caseID int64) (int, error) {
	if r == nil || r.source == nil {
		return 0, fmt.Errorf("judge rework budget reader is not configured")
	}
	items, err := r.source.ReworkItemsForCase(ctx, versionID, caseID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, item := range items {
		if agentapp.WorkTypeForItem(item) != designapp.TaskTypeDesignRework {
			continue
		}
		payload, err := agentapp.ParseWorkItemPayload(item.Payload)
		if err != nil || payload.CaseID != caseID {
			continue
		}
		if designWorkItemTerminal(item) {
			count++
		}
	}
	return count, nil
}

// HasTerminalReworkForVersion reports whether the design.rework bound to this
// FAILED case version is terminal (done, dead, or failed at its attempt cap —
// the SAME budget-consuming predicate as the count, one authority). The rework
// request key is rework:<case>:<failedVersion> with no generation and the
// work-item store never revives a terminal row, so a terminal rework means no
// further rework can ever respond to this fail — the case must escalate to a
// human even when the round budget still has room. Both endings are covered: a
// rework that DIED, and a rework that completed DONE WITHOUT proposing a
// revision (a run may end without a verified completion), which leaves the fail
// bound to the current version. A rework that DID revise advances the current
// version, so the fail is no longer bound to it and the normal loop never reaches
// this branch. A nil source fails closed.
func (r *judgeReworkBudgetReader) HasTerminalReworkForVersion(ctx context.Context, versionID, caseID, caseVersionID int64) (bool, error) {
	if r == nil || r.source == nil {
		return false, fmt.Errorf("judge rework budget reader is not configured")
	}
	items, err := r.source.ReworkItemsForCase(ctx, versionID, caseID)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if agentapp.WorkTypeForItem(item) != designapp.TaskTypeDesignRework {
			continue
		}
		payload, err := agentapp.ParseWorkItemPayload(item.Payload)
		if err != nil || payload.CaseID != caseID || payload.CaseVersionID != caseVersionID {
			continue
		}
		if designWorkItemTerminal(item) {
			return true, nil
		}
	}
	return false, nil
}

// designWorkItemTerminal is THE single predicate for "this design work item is
// terminal": it finished (done), it died (dead), or it failed with its attempts
// at the cap. Every design scheduling decision that needs the lifecycle asks
// THIS function — the rework escalation budget (how much budget the case spent,
// and whether the rework bound to one failed version can still produce a
// revision), the coverage-repair and judge generation counters (whether a
// generation still owes work), and the authoring-batch settle state (H-F6).
// WorkItemStatus.Terminal() alone is NOT this predicate: it is also true for a
// retryable failed item, which the queue will claim again and which therefore
// still owes its work. In-flight statuses (open/claimed/running/waiting_human)
// are never terminal here.
// The no-permanent-pending guarantee depends on design.rework NEVER entering
// waiting_human: it is non-terminal here but has no automatic settlement path
// (RecoverExpired only scans claimed/running), so a rework parked there would
// neither settle nor escalate. Today design.rework has no waiting_human writer.
// design.requirement CAN in principle park there too. That is deliberately NOT
// treated as a stall (review I1): a human takeover has no deadline, so the
// authoring batch's stall clock only runs on lease-holding rows
// (workItemHoldsLease) — a parked or queued row defers the judge without ever
// escalating it. Today design.requirement has no waiting_human writer either.
func designWorkItemTerminal(item agent.WorkItem) bool {
	switch item.Status {
	case agent.WorkItemDone, agent.WorkItemDead:
		return true
	case agent.WorkItemFailed:
		return item.Attempts >= item.MaxAttempts
	default:
		return false
	}
}

// judgeReworkSQLSource reads the design.rework rows of one case directly from
// work_item (read-only, the same composition-root SQL-source pattern as
// judgeExhaustedFactStore.Has). The (version_id, type) prefix rides the
// uk_work_item_dedupe index; payload->>'case_id' normalizes JSON string and
// number IDs to the same decimal text. Rework items are 0.2.1 TYPE-path rows
// (kind is empty, T8 ruling); the kind guard keeps the WorkTypeForItem
// convention (kind authoritative when present) without giving up the index
// prefix. No LIMIT window: every rework row of the case is returned, newest first.
// attempts/max_attempts are read so the escalation-budget reader can tell a
// retry-exhausted failure from a retryable one (the provenance reader ignores
// them: a request counts from creation regardless of status).
type judgeReworkSQLSource struct {
	pool *pgxpool.Pool
}

func (s *judgeReworkSQLSource) ReworkItemsForCase(ctx context.Context, versionID, caseID int64) ([]agent.WorkItem, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("judge rework work-item SQL source is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, type, kind, status, attempts, max_attempts, payload, created_at
		FROM work_item
		WHERE version_id = $1 AND type = $2 AND (kind = '' OR kind = $2)
		  AND payload->>'case_id' = $3
		ORDER BY created_at DESC, id DESC
	`, versionID, designapp.TaskTypeDesignRework, platformid.Format(caseID))
	if err != nil {
		return nil, fmt.Errorf("read design rework work items: %w", err)
	}
	defer rows.Close()
	items := make([]agent.WorkItem, 0)
	for rows.Next() {
		var item agent.WorkItem
		if err := rows.Scan(&item.ID, &item.Type, &item.Kind, &item.Status,
			&item.Attempts, &item.MaxAttempts, &item.Payload, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan design rework work item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read design rework work items: %w", err)
	}
	return items, nil
}
