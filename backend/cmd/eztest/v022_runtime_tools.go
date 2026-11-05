package main

// v0.22 W0 runtime tool ports (Task 36).
//
// The composition root binds the additive W0 Host tools and the tool-result
// content governance to the existing version/execution services. Everything
// here is a thin adapter: the tools package keeps the model-visible contract and
// the version application keeps the validated write use cases.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

/* ------------------------------ settings --------------------------------- */

// v022DefaultWindowTokens is the H2 design default of context.window_tokens
// (spec §4.3): 128000. The settings catalog already seeds this value; this
// wrapper makes the runtime consistent even if the row is missing or
// unreadable, so the retired 64000 fallback in the agent context builder can
// never be reached through the production runner.
const v022DefaultWindowTokens = 128000

// windowTokenSettingsReader is the narrow settings read surface of the runtime
// wrapper (satisfied by *settingsapp.Service and by test fakes).
type windowTokenSettingsReader interface {
	Int(ctx context.Context, key string) (int64, error)
	Float(ctx context.Context, key string) (float64, error)
}

// windowTokensSettings wraps the settings service for the agent runner and
// enforces the H2 window-token default on the one key the context builder
// reads. Every other read delegates unchanged.
type windowTokensSettings struct {
	reader windowTokenSettingsReader
}

func (s windowTokensSettings) Int(ctx context.Context, key string) (int64, error) {
	if key == "context.window_tokens" {
		if value, err := s.reader.Int(ctx, key); err == nil && value > 0 {
			return value, nil
		}
		return v022DefaultWindowTokens, nil
	}
	return s.reader.Int(ctx, key)
}

func (s windowTokensSettings) Float(ctx context.Context, key string) (float64, error) {
	return s.reader.Float(ctx, key)
}

/* ------------------------------ plan.update ------------------------------ */

// init registers the v0.22 task.complete tool group once per process. The group
// only materializes when the per-run CompletionDeps are present, so a run whose
// completion target could not be resolved never exposes task.complete.
func init() {
	tools.RegisterP4ToolGroup(tools.TaskCompleteTools)
}

// planWriterAdapter maps the plan.update tool's write port onto
// versionapp.PlanService.Update (R-A8-2 optimistic revision write).
type planWriterAdapter struct {
	plans *versionapp.PlanService
}

var _ tools.PlanWriter = planWriterAdapter{}

func (a planWriterAdapter) Update(ctx context.Context, versionID, itemID int64, patch tools.PlanPatch, revision int) (tools.PlanItem, error) {
	if a.plans == nil {
		return tools.PlanItem{}, errors.New("plan writer: plan service is not configured")
	}
	item, err := a.plans.Update(ctx, versionID, itemID, versiondomain.PlanPatch{
		Title:    patch.Title,
		Status:   patch.Status,
		Decision: patch.Decision,
		Order:    patch.Order,
	}, revision)
	if err != nil {
		if errors.Is(err, versiondomain.ErrPlanRevisionConflict) {
			return tools.PlanItem{}, &tools.PlanRevisionConflict{ItemID: itemID, Expected: revision}
		}
		return tools.PlanItem{}, err
	}
	return tools.PlanItem{
		ID:         item.ID,
		VersionID:  item.VersionID,
		Phase:      item.Phase,
		Title:      item.Title,
		Status:     item.Status,
		DependsOn:  append([]int64(nil), item.DependsOn...),
		WorkItemID: item.WorkItemID,
		Decision:   item.Decision,
		Order:      item.Order,
		Revision:   item.Revision,
	}, nil
}

/* ------------------------------ question.ask ----------------------------- */

// questionAskerAdapter maps question.ask onto the structured-question service
// (Task 23). *executionapp.QuestionService satisfies the tool port directly.
type questionAskerAdapter struct {
	questions *executionapp.QuestionService
}

var _ tools.QuestionAsker = questionAskerAdapter{}

func (a questionAskerAdapter) AskQuestions(ctx context.Context, scope tools.Scope, in tools.QuestionAskInput) (tools.QuestionAskResult, error) {
	if a.questions == nil {
		return tools.QuestionAskResult{}, errors.New("question asker: question service is not configured")
	}
	return a.questions.AskFromTool(ctx, scope, in)
}

// questionMessageWriterAdapter is the version conversation write port of the
// structured-question service (R-B3-3/R-B3-5). It writes the question card and
// the answer echo as conversation_message rows with kind=question/answer. The
// (conversation_id, client_message_id) unique key makes a retried write return
// the already-stored message id with created=false.
type questionMessageWriterAdapter struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

var _ executionapp.QuestionMessageWriter = questionMessageWriterAdapter{}

func (a questionMessageWriterAdapter) AppendQuestionMessage(ctx context.Context, message executionapp.QuestionMessage) (int64, bool, error) {
	if a.pool == nil || a.ids == nil {
		return 0, false, errors.New("question message writer: pool or id generator is not configured")
	}
	if message.ConversationID <= 0 || message.VersionID <= 0 {
		return 0, false, errors.New("question message writer: version and conversation are required")
	}
	kind := strings.TrimSpace(message.Kind)
	if kind != executionapp.QuestionMessageKindQuestion && kind != executionapp.QuestionMessageKindAnswer {
		return 0, false, fmt.Errorf("question message writer: unsupported kind %q", message.Kind)
	}
	clientMessageID := strings.TrimSpace(message.ClientMessageID)
	if clientMessageID == "" {
		clientMessageID = fmt.Sprintf("question-%s-%d", kind, message.QuestionID)
	}
	role := "assistant"
	if kind == executionapp.QuestionMessageKindAnswer {
		role = "user"
	}
	partType := "question_card"
	if kind == executionapp.QuestionMessageKindAnswer {
		partType = "text"
	}
	parts, err := json.Marshal([]map[string]any{{
		"type": partType,
		"data": message.Payload,
	}})
	if err != nil {
		return 0, false, err
	}
	messageID := a.ids.Next()
	const query = `
INSERT INTO conversation_message
    (id, conversation_id, role, kind, parts, actor_id, agent_run_id, client_message_id, created_at)
SELECT $1, c.id, $2, $3, $4::jsonb, NULLIF($5, 0), NULLIF($6, 0), $7, now()
FROM conversation c
WHERE c.id = $8 AND c.version_id = $9
ON CONFLICT (conversation_id, client_message_id)
DO UPDATE SET kind = EXCLUDED.kind
RETURNING id, (xmax = 0) AS created`
	var storedID int64
	var created bool
	if err := a.pool.QueryRow(ctx, query,
		messageID, role, kind, parts, message.ActorID, message.AgentRunID,
		clientMessageID, message.ConversationID, message.VersionID,
	).Scan(&storedID, &created); err != nil {
		return 0, false, err
	}
	if created {
		if _, err := a.pool.Exec(ctx,
			`UPDATE conversation SET updated_at = now() WHERE id = $1`, message.ConversationID); err != nil {
			return 0, false, err
		}
	}
	return storedID, created, nil
}

/* --------------------------- tool result content ------------------------- */

// toolContentStore implements the tools content store over the version
// adapter's spill writer and the storage read path (R-A6-1).
type toolContentStore struct {
	adapter *versionadapter.Adapter
	storage *storageapp.Service
}

var _ tools.ContentStore = toolContentStore{}

const maxToolResultReadBytes = 4 << 20

func (s toolContentStore) PutText(ctx context.Context, owner string, data []byte) (tools.ContentRef, error) {
	if s.adapter == nil {
		return tools.ContentRef{}, errors.New("tool content store: version adapter is not configured")
	}
	ref, err := s.adapter.PutText(ctx, owner, data)
	if err != nil {
		return tools.ContentRef{}, err
	}
	return tools.ContentRef{Ref: ref.Ref, SHA256: ref.SHA256, Size: len(data)}, nil
}

func (s toolContentStore) ReadText(ctx context.Context, contentRef string) (string, error) {
	if s.storage == nil {
		return "", errors.New("tool content store: storage service is not configured")
	}
	_, reader, err := s.storage.Open(ctx, strings.TrimSpace(contentRef))
	if err != nil {
		return "", err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxToolResultReadBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxToolResultReadBytes {
		return "", fmt.Errorf("tool content store: content %s exceeds the read cap", contentRef)
	}
	return string(data), nil
}

/* ------------------------------ completion ------------------------------- */

// completionPlanItemReader resolves the optimistic-lock snapshot of one plan
// item for the plan-item completion criterion.
type completionPlanItemReader struct {
	plans *versionapp.PlanService
}

func (r completionPlanItemReader) GetPlanItem(ctx context.Context, versionID, itemID int64) (tools.PlanItemSnapshot, error) {
	if r.plans == nil {
		return tools.PlanItemSnapshot{}, errors.New("plan item reader: plan service is not configured")
	}
	items, err := r.plans.List(ctx, versionID, "")
	if err != nil {
		return tools.PlanItemSnapshot{}, err
	}
	for _, item := range items {
		if item.ID == itemID {
			return tools.PlanItemSnapshot{
				ID:        item.ID,
				VersionID: item.VersionID,
				Status:    item.Status,
				Decision:  item.Decision,
				Revision:  item.Revision,
			}, nil
		}
	}
	return tools.PlanItemSnapshot{}, versiondomain.ErrNotFound
}

// hostTurnCompletionReader answers the Host completion criterion: the turn's
// outstanding structured question must be answered (or handed off), which for
// v0.22 means the version has no pending clarification question left.
type hostTurnCompletionReader struct {
	questions *executionapp.QuestionService
}

func (r hostTurnCompletionReader) TurnAnswered(ctx context.Context, scope tools.Scope) (bool, error) {
	if r.questions == nil {
		return false, errors.New("host turn reader: question service is not configured")
	}
	pending, _, err := r.questions.List(ctx, scope.VersionID, string(execution.HumanPending), 100, nil)
	if err != nil {
		return false, err
	}
	return len(pending) == 0, nil
}

// designRevisionCompletionReader answers the design completion criterion: at
// least one current case revision/review artifact exists for the version.
type designRevisionCompletionReader struct {
	graph *designapp.GraphService
}

func (r designRevisionCompletionReader) DesignRevisionExists(ctx context.Context, versionID int64) (bool, error) {
	if r.graph == nil {
		return false, errors.New("design revision reader: design graph is not configured")
	}
	graph, err := r.graph.ReadGraph(ctx, versionID)
	if err != nil {
		return false, err
	}
	for _, requirement := range graph.Requirements {
		for _, scenario := range requirement.Scenarios {
			for _, testCase := range scenario.Cases {
				if testCase.CurrentVersionID > 0 {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// completionStore persists the verified completion write set: the target plan
// item status/decision and the progress fact (R-A3-4/R-A8-3). It uses the
// application services' validated writes; the plan write is optimistic and the
// fact append is idempotent, so a retried completion converges.
type completionStore struct {
	plans *versionapp.PlanService
	facts *versionapp.FactService
}

var _ agentapp.CompletionStore = completionStore{}

func (s completionStore) CommitCompletion(ctx context.Context, write agentapp.CompletionWrite) (agentapp.CompletionWriteResult, error) {
	if s.plans == nil || s.facts == nil {
		return agentapp.CompletionWriteResult{}, errors.New("completion store: plan/fact services are not configured")
	}
	items, err := s.plans.List(ctx, write.VersionID, "")
	if err != nil {
		return agentapp.CompletionWriteResult{}, err
	}
	revision := 0
	for _, item := range items {
		if item.ID == write.PlanItemID {
			revision = item.Revision
			break
		}
	}
	if revision <= 0 {
		return agentapp.CompletionWriteResult{}, fmt.Errorf("completion store: plan item %d not found in version %d", write.PlanItemID, write.VersionID)
	}
	item, err := s.plans.Update(ctx, write.VersionID, write.PlanItemID, versiondomain.PlanPatch{
		Status:   write.PlanStatus,
		Decision: write.Decision,
	}, revision)
	if err != nil {
		return agentapp.CompletionWriteResult{}, err
	}
	outcome, err := s.facts.RecordFact(ctx, versiondomain.Fact{
		VersionID: write.VersionID,
		Kind:      write.FactKind,
		Key:       write.FactKey,
		Payload:   write.FactPayload,
		Source:    write.Source,
		SourceRef: write.SourceRef,
	})
	if err != nil {
		return agentapp.CompletionWriteResult{}, err
	}
	return agentapp.CompletionWriteResult{FactID: outcome.Fact.ID, PlanItemRevision: item.Revision}, nil
}

// completionBuilder is the per-run task.complete factory. It resolves the one
// plan item linked to the run's work item and binds the completion criterion of
// the profile. Every completion targets a plan item (R-A3-4); a work item with
// no linked plan item leaves task.complete unregistered rather than exposing a
// fact-only completion.
type completionBuilder struct {
	plans     *versionapp.PlanService
	graph     *designapp.GraphService
	questions *executionapp.QuestionService
	store     agentapp.CompletionStore
}

func (b completionBuilder) Build(ctx context.Context, run agentapp.CompletionContext) (tools.CompletionDeps, error) {
	if b.plans == nil || b.store == nil {
		return tools.CompletionDeps{}, errors.New("completion builder: plan service or store is not configured")
	}
	items, err := b.plans.List(ctx, run.VersionID, "")
	if err != nil {
		return tools.CompletionDeps{}, err
	}
	var target *versiondomain.PlanItem
	for index := range items {
		item := items[index]
		if item.WorkItemID != nil && *item.WorkItemID == run.WorkItemID {
			target = &item
			break
		}
	}
	if target == nil {
		return tools.CompletionDeps{}, fmt.Errorf("completion builder: no plan item is linked to work item %d", run.WorkItemID)
	}
	reader := completionPlanItemReader{plans: b.plans}
	var validator tools.Validator
	switch run.ProfileKey {
	case agentapp.ProfileKeyClarificationHost, agentapp.ProfileKeyDesignHost:
		validator = tools.NewHostCompletionValidator(hostTurnCompletionReader{questions: b.questions}, target.ID)
	case agentapp.ProfileKeyDesignAuthor, agentapp.ProfileKeyDesignCritic:
		validator = tools.NewDesignCompletionValidator(designRevisionCompletionReader{graph: b.graph}, target.ID)
	default:
		validator = tools.NewPlanItemCompletionValidator(reader, target.ID)
	}
	return tools.CompletionDeps{
		Validator: validator,
		Writer:    agentapp.NewCompletionService(b.store),
	}, nil
}

func (b completionBuilder) completionDeps(ctx context.Context, run agentapp.CompletionContext) (tools.CompletionDeps, error) {
	return b.Build(ctx, run)
}
