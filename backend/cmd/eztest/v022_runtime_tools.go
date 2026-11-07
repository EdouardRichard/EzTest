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
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
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
// structured-question service (R-B3-3/R-B3-5). It replaces the pre-fix direct
// SQL INSERT with the ConversationService structured-message API: the question
// card is authored by the live Host writer lease (author_agent) and written as
// a domain-valid `question` part, while the answer echo is an idempotent user
// message. The (conversation_id, client_message_id) unique key makes a retried
// write return the already-stored message.
type questionMessageWriterAdapter struct {
	conversations structuredConversationWriter
}

// structuredConversationWriter is the narrow version-conversation surface the
// adapter needs (*versionapp.ConversationService in production).
type structuredConversationWriter interface {
	AppendStructuredMessage(ctx context.Context, in versionapp.StructuredMessageInput) (versionapp.MessageView, bool, error)
}

var _ executionapp.QuestionMessageWriter = questionMessageWriterAdapter{}

func (a questionMessageWriterAdapter) AppendQuestionMessage(ctx context.Context, message executionapp.QuestionMessage) (int64, bool, error) {
	if a.conversations == nil {
		return 0, false, errors.New("question message writer: conversation service is not configured")
	}
	if message.ConversationID <= 0 || message.VersionID <= 0 {
		return 0, false, errors.New("question message writer: version and conversation are required")
	}
	kind := strings.TrimSpace(message.Kind)
	if kind != executionapp.QuestionMessageKindQuestion && kind != executionapp.QuestionMessageKindAnswer {
		return 0, false, fmt.Errorf("question message writer: unsupported kind %q", message.Kind)
	}
	clientMessageID := strings.TrimSpace(message.ClientMessageID)
	parts, err := questionMessageParts(kind, message)
	if err != nil {
		return 0, false, err
	}
	view, created, err := a.conversations.AppendStructuredMessage(ctx, versionapp.StructuredMessageInput{
		VersionID:       message.VersionID,
		ConversationID:  message.ConversationID,
		Kind:            kind,
		Parts:           parts,
		ClientMessageID: clientMessageID,
		ActorID:         message.ActorID,
		AgentRunID:      message.AgentRunID,
	})
	if err != nil {
		return 0, false, err
	}
	return view.ID, created, nil
}

// questionMessageParts renders the domain parts of one structured message:
// the question card is the `question` part (flat payload), the answer echo is
// a bounded text summary of the submitted answers (the question card itself is
// the authoritative UI snapshot and is keyed by the same question_id).
func questionMessageParts(kind string, message executionapp.QuestionMessage) ([]versiondomain.MessagePart, error) {
	if kind == executionapp.QuestionMessageKindQuestion {
		payload, err := json.Marshal(message.Payload)
		if err != nil {
			return nil, err
		}
		return []versiondomain.MessagePart{{
			Type:     versiondomain.MessagePartQuestion,
			Question: payload,
		}}, nil
	}
	return []versiondomain.MessagePart{{
		Type: versiondomain.MessagePartText,
		Text: questionAnswerEchoText(message.Payload),
	}}, nil
}

// questionAnswerEchoText is the bounded user-facing text of one answer echo.
// The structured payload remains on the question card/API; this message only
// closes the thread with the submitted answer.
func questionAnswerEchoText(payload map[string]any) string {
	const maxRunes = 1500
	builder := strings.Builder{}
	builder.WriteString("已提交问题答案")
	if questionID, ok := payload["question_id"].(string); ok && strings.TrimSpace(questionID) != "" {
		builder.WriteString("（question_id=")
		builder.WriteString(strings.TrimSpace(questionID))
		builder.WriteString("）")
	}
	if rawAnswers, ok := payload["answers"].([]any); ok {
		for _, rawAnswer := range rawAnswers {
			answer, ok := rawAnswer.(map[string]any)
			if !ok {
				continue
			}
			line := answerEchoLine(answer)
			if line == "" {
				continue
			}
			builder.WriteString("\n- ")
			builder.WriteString(line)
		}
	}
	text := builder.String()
	if runes := []rune(text); len(runes) > maxRunes {
		text = string(runes[:maxRunes])
	}
	return text
}

func answerEchoLine(answer map[string]any) string {
	questionID, _ := answer["question_id"].(string)
	text, _ := answer["text"].(string)
	selected := make([]string, 0)
	if rawSelected, ok := answer["selected"].([]any); ok {
		for _, value := range rawSelected {
			if label, ok := value.(string); ok && strings.TrimSpace(label) != "" {
				selected = append(selected, strings.TrimSpace(label))
			}
		}
	}
	parts := make([]string, 0, 3)
	if value := strings.TrimSpace(questionID); value != "" {
		parts = append(parts, value+":")
	}
	if len(selected) > 0 {
		parts = append(parts, strings.Join(selected, ", "))
	}
	if value := strings.TrimSpace(text); value != "" {
		parts = append(parts, value)
	}
	return strings.Join(parts, " ")
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

// planCompletionCatalog is the narrow plan read/write surface the completion
// wiring needs. *versionapp.PlanService satisfies it; tests inject a fake so the
// composition-root target resolver is testable without a database.
type planCompletionCatalog interface {
	List(ctx context.Context, versionID int64, status string) ([]versiondomain.PlanItem, error)
	Update(ctx context.Context, versionID, itemID int64, patch versiondomain.PlanPatch, revision int) (versiondomain.PlanItem, error)
	// Upsert creates the per-run Host plan item when the production plan
	// creation path has not seeded one yet (W0 fallback; W1 replaces this with
	// plan-driven Host turns). The work-item link keeps resolution stable.
	Upsert(ctx context.Context, item versiondomain.PlanItem) (versiondomain.PlanItem, error)
}

// completionPlanItemReader resolves the optimistic-lock snapshot of one plan
// item for the plan-item completion criterion.
type completionPlanItemReader struct {
	plans planCompletionCatalog
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
	return pendingClarificationCount(pending) == 0, nil
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

// completionBuilder is the per-run task.complete factory. It resolves the one
// plan item the verified completion writes and binds the completion criterion of
// the profile. Every completion targets a plan item (R-A3-4); a run whose target
// cannot be resolved leaves task.complete unregistered rather than exposing a
// fact-only completion.
//
// Target resolution is deterministic and ordered:
//  1. a plan item whose work_item_id is the run's work item (the explicit link);
//  2. the plan_item_id carried by the work-item payload (a Host/design work item
//     created by a plan-driven path);
//  3. the version's single in-progress plan item (the production fallback while
//     the plan write path is still zero rows). More than one in-progress item is
//     ambiguous and fails closed.
type completionBuilder struct {
	plans     planCompletionCatalog
	graph     *designapp.GraphService
	questions *executionapp.QuestionService
	store     agentapp.CompletionStore
}

func (b completionBuilder) Build(ctx context.Context, run agentapp.CompletionContext) (tools.CompletionDeps, error) {
	if b.plans == nil || b.store == nil {
		return tools.CompletionDeps{}, errors.New("completion builder: plan catalog or store is not configured")
	}
	target, err := b.resolveTarget(ctx, run)
	if err != nil {
		return tools.CompletionDeps{}, err
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

// BuildSettlement resolves the settlement writeback path of one run (R-A8-3,
// A §6.12 rule 3). It reuses resolveTarget, so the plan item is the same
// ordered choice task.complete would write (work-item link → payload
// plan_item_id → the version's single in-progress item); a fact-only fallback
// does not exist. The runner calls it only when a run actually ends on the
// turn/time settlement turn without a verified completion, and treats an error
// as best-effort (log-only), so a version without a plan item never fails a run.
func (b completionBuilder) BuildSettlement(ctx context.Context, run agentapp.CompletionContext) (agentapp.SettlementRecorder, error) {
	if b.plans == nil || b.store == nil {
		return nil, errors.New("settlement builder: plan catalog or store is not configured")
	}
	target, err := b.resolveTarget(ctx, run)
	if err != nil {
		return nil, err
	}
	return agentapp.NewSettlementService(b.store, target.ID), nil
}

// resolveTarget applies the ordered completion target resolution documented on
// completionBuilder. It never guesses between two candidates.
func (b completionBuilder) resolveTarget(ctx context.Context, run agentapp.CompletionContext) (versiondomain.PlanItem, error) {
	if run.VersionID <= 0 {
		return versiondomain.PlanItem{}, errors.New("completion builder: run has no version")
	}
	items, err := b.plans.List(ctx, run.VersionID, "")
	if err != nil {
		return versiondomain.PlanItem{}, fmt.Errorf("completion builder: list plan items: %w", err)
	}
	if run.WorkItemID > 0 {
		for _, item := range items {
			if item.WorkItemID != nil && *item.WorkItemID == run.WorkItemID {
				return item, nil
			}
		}
	}
	if run.PlanItemID > 0 {
		for _, item := range items {
			if item.ID == run.PlanItemID {
				return item, nil
			}
		}
		return versiondomain.PlanItem{}, fmt.Errorf(
			"completion builder: plan item %d from the work-item payload is not in version %d",
			run.PlanItemID, run.VersionID)
	}
	inProgress := make([]versiondomain.PlanItem, 0, 1)
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.Status), "in_progress") {
			inProgress = append(inProgress, item)
		}
	}
	switch len(inProgress) {
	case 1:
		return inProgress[0], nil
	case 0:
		if run.WorkItemID > 0 {
			// W0 fallback: the production plan-item creation side is not yet
			// wired for Host turns. Create one item per work item so the
			// completion criterion (task.complete + server validation) is
			// reachable; the work-item link makes later resolution stable.
			phase := "clarifying"
			title := "Host 回合"
			switch run.ProfileKey {
			case agentapp.ProfileKeyDesignHost, agentapp.ProfileKeyDesignAuthor, agentapp.ProfileKeyDesignCritic:
				phase = "designing"
				title = "设计 Host 回合"
			case agentapp.ProfileKeyCaseExecutor:
				phase = "executing"
				title = "执行回合"
			}
			workItemID := run.WorkItemID
			created, createErr := b.plans.Upsert(ctx, versiondomain.PlanItem{
				VersionID:  run.VersionID,
				Phase:      phase,
				Title:      fmt.Sprintf("%s #%d", title, workItemID),
				Status:     "in_progress",
				WorkItemID: &workItemID,
			})
			if createErr != nil {
				return versiondomain.PlanItem{}, fmt.Errorf("completion builder: create Host plan item: %w", createErr)
			}
			return created, nil
		}
		return versiondomain.PlanItem{}, fmt.Errorf(
			"completion builder: no plan item is linked to work item %d and version %d has no in-progress plan item",
			run.WorkItemID, run.VersionID)
	default:
		return versiondomain.PlanItem{}, fmt.Errorf(
			"completion builder: version %d has %d in-progress plan items; cannot choose a completion target deterministically",
			run.VersionID, len(inProgress))
	}
}

func (b completionBuilder) completionDeps(ctx context.Context, run agentapp.CompletionContext) (tools.CompletionDeps, error) {
	return b.Build(ctx, run)
}
