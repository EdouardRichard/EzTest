package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Tool context bounds (design spec §8.3).
const (
	// maxToolContextBytes bounds one version.context_get payload.
	maxToolContextBytes = 32 << 10
	// maxToolCodeTreeEntries bounds one code.tree payload.
	maxToolCodeTreeEntries = 200
	// maxToolCodeReadBytes bounds one code.read result.
	maxToolCodeReadBytes = 64 << 10
	// maxToolCodeReadOffset is a sanity bound on code.read's byte offset so a
	// model-supplied offset cannot force the reader to discard an unbounded
	// stream.
	maxToolCodeReadOffset = 1 << 20
	// context page/collection bounds. The total payload is additionally capped
	// by maxToolContextBytes.
	contextRequirementPageSize = 100
	contextRequirementLimit    = 200
	contextQuestionLimit       = 100
	contextDecisionLimit       = 100
	contextAttachmentLimit     = 200
	contextStringTrimRunes     = 2000
	contextSummaryTrimRunes    = 4000
)

// AgentToolService exposes the 11 structured primitives behind the P3 agent
// tools (design spec §8.3). The tool adapters live in the P1 tools package and
// depend only on a local narrow port; this service reuses the P3 requirement,
// relation, clarification and attachment use cases and never imports P1
// infrastructure.
//
// Every primitive returns (content, isError, error): content is the
// model-facing JSON (or a correctable reason when isError is true), while a
// non-nil error is an infrastructure failure the P1 executor records as a
// failed tool call.
type AgentToolService struct {
	versions             VersionRepository
	requirements         RequirementRepository
	clarifications       ClarificationRepository
	requirementService   *RequirementService
	relationService      *RelationService
	clarificationService *ClarificationService
	attachmentService    *AttachmentService
	resources            ResourceCatalog
	git                  GitReader
}

// AgentToolServiceDeps collects the P3 services and ports the tool service
// consumes. T16 constructs it once and injects it into the agent tools.
type AgentToolServiceDeps struct {
	Versions             VersionRepository
	Requirements         RequirementRepository
	Clarifications       ClarificationRepository
	RequirementService   *RequirementService
	RelationService      *RelationService
	ClarificationService *ClarificationService
	AttachmentService    *AttachmentService
	Resources            ResourceCatalog
	Git                  GitReader
}

// NewAgentToolService wires the tool primitives to their services and ports.
func NewAgentToolService(deps AgentToolServiceDeps) *AgentToolService {
	return &AgentToolService{
		versions:             deps.Versions,
		requirements:         deps.Requirements,
		clarifications:       deps.Clarifications,
		requirementService:   deps.RequirementService,
		relationService:      deps.RelationService,
		clarificationService: deps.ClarificationService,
		attachmentService:    deps.AttachmentService,
		resources:            deps.Resources,
		git:                  deps.Git,
	}
}

// ContextGet returns the bounded version context consumed by all three P3
// profiles: version, requirements with open-question counts, relations,
// open/answered questions, proposed/accepted decisions, attachment metadata and
// the active session's context summary (design spec §8.3 version.context_get).
func (s *AgentToolService) ContextGet(ctx context.Context, versionID int64) (string, bool, error) {
	if s.versions == nil {
		return "", false, errors.New("version: agent tool service is not configured")
	}
	version, err := s.versions.Get(ctx, versionID)
	if err != nil {
		return toolOutcome(err)
	}
	counts, err := s.versions.Counts(ctx, versionID)
	if err != nil {
		return toolOutcome(err)
	}
	view := toolContextView{
		Version: toolVersionView{
			ID:                 formatID(version.ID),
			Name:               version.Name,
			Label:              version.Label,
			Description:        version.Description,
			Status:             version.Status,
			Revision:           version.Revision,
			SystemID:           version.SystemID,
			EnvironmentID:      version.EnvironmentID,
			CodeRepositoryID:   version.CodeRepositoryID,
			CodeBranch:         stringValue(version.CodeBranch),
			CodeBaselineCommit: stringValue(version.CodeBaselineCommit),
			Counts: toolVersionCounts{
				RequirementCount:  counts.RequirementCount,
				ClarifiedCount:    counts.ClarifiedCount,
				OpenQuestionCount: counts.OpenQuestionCount,
			},
		},
		Requirements: []toolRequirementView{},
		Relations:    []toolRelationView{},
		Questions:    []toolQuestionView{},
		Decisions:    []toolDecisionView{},
		Attachments:  []toolAttachmentView{},
	}

	requirements, requirementsMore, err := s.contextRequirements(ctx, versionID)
	if err != nil {
		return toolOutcome(err)
	}
	if requirementsMore {
		view.Truncated = true
	}
	for i := range requirements {
		requirement := &requirements[i]
		openCount := 0
		if s.requirements != nil {
			openCount, err = s.requirements.CountOpenQuestions(ctx, requirement.ID)
			if err != nil {
				return toolOutcome(err)
			}
		}
		view.Requirements = append(view.Requirements, toolRequirementView{
			ID:                 formatID(requirement.ID),
			Title:              requirement.Title,
			Status:             requirement.Status,
			Description:        requirement.Description,
			AcceptanceCriteria: requirement.AcceptanceCriteria,
			Priority:           requirement.Priority,
			Revision:           requirement.Revision,
			OpenQuestionCount:  openCount,
		})
	}

	relations, err := s.contextRelations(ctx, versionID)
	if err != nil {
		return toolOutcome(err)
	}
	for i := range relations {
		view.Relations = append(view.Relations, toolRelationView{
			ID:                formatID(relations[i].ID),
			FromRequirementID: formatID(relations[i].FromRequirementID),
			ToRequirementID:   formatID(relations[i].ToRequirementID),
			RelationType:      relations[i].RelationType,
			Description:       relations[i].Description,
		})
	}

	questions, questionsMore, err := s.contextQuestions(ctx, versionID)
	if err != nil {
		return toolOutcome(err)
	}
	if questionsMore {
		view.Truncated = true
	}
	for i := range questions {
		question := &questions[i]
		view.Questions = append(view.Questions, toolQuestionView{
			ID:            formatID(question.ID),
			RequirementID: optionalID(question.RequirementID),
			Question:      question.Question,
			Status:        question.Status,
			Answer:        question.Answer,
			Source:        question.Source,
		})
	}

	decisions, decisionsMore, err := s.contextDecisions(ctx, versionID)
	if err != nil {
		return toolOutcome(err)
	}
	if decisionsMore {
		view.Truncated = true
	}
	for i := range decisions {
		decision := &decisions[i]
		view.Decisions = append(view.Decisions, toolDecisionView{
			ID:            formatID(decision.ID),
			RequirementID: optionalID(decision.RequirementID),
			QuestionID:    optionalID(decision.QuestionID),
			Topic:         decision.Topic,
			Decision:      decision.Decision,
			Rationale:     decision.Rationale,
			Status:        decision.Status,
		})
	}

	attachments, attachmentsMore, err := s.contextAttachments(ctx, versionID)
	if err != nil {
		return toolOutcome(err)
	}
	if attachmentsMore {
		view.Truncated = true
	}
	view.Attachments = attachments

	if s.clarifications != nil {
		session, err := s.clarifications.GetActiveSession(ctx, versionID)
		if err != nil {
			return toolOutcome(err)
		}
		if session != nil {
			view.ContextSummary = session.ContextSummary
		}
	}

	content, err := fitToolContext(view)
	if err != nil {
		return "", false, err
	}
	return content, false, nil
}

// UpdateDraft applies the AI-whitelisted patch to a draft requirement under the
// revision CAS and records last_update_source=ai (design spec §6.2/§8.3).
func (s *AgentToolService) UpdateDraft(ctx context.Context, versionID, requirementID int64, revision int, patch json.RawMessage, actor int64) (string, bool, error) {
	if s.requirementService == nil {
		return "", false, errors.New("version: requirement service is not configured")
	}
	command, err := decodeDraftPatch(revision, patch)
	if err != nil {
		return toolOutcome(err)
	}
	requirement, err := s.requirementService.Update(ctx, versionID, requirementID, command, actor)
	if err != nil {
		return toolOutcome(err)
	}
	return toolJSON(toolRequirementDetailView{
		ID:                 formatID(requirement.ID),
		VersionID:          formatID(requirement.VersionID),
		Title:              requirement.Title,
		Description:        requirement.Description,
		AcceptanceCriteria: requirement.AcceptanceCriteria,
		Priority:           requirement.Priority,
		Status:             requirement.Status,
		Revision:           requirement.Revision,
		Source:             requirement.Source,
		LastUpdateSource:   requirement.LastUpdateSource,
	})
}

// QuestionUpsert creates or rewrites one AI-authored open question. The
// clarification service makes the create idempotent for an equal open question
// (design spec §6.6/§8.3).
func (s *AgentToolService) QuestionUpsert(ctx context.Context, versionID int64, questionID, requirementID *int64, question string, actor int64) (string, bool, error) {
	if s.clarificationService == nil {
		return "", false, errors.New("version: clarification service is not configured")
	}
	created, err := s.clarificationService.UpsertQuestion(ctx, versionID, QuestionInput{
		QuestionID:    questionID,
		RequirementID: requirementID,
		Question:      question,
	}, actor, domain.SourceAI)
	if err != nil {
		return toolOutcome(err)
	}
	return toolJSON(map[string]string{"questionId": formatID(created.ID)})
}

// QuestionAnswer records an explicit user answer transcribed by the agent
// (answered_via=agent). The agent may never dismiss a question.
func (s *AgentToolService) QuestionAnswer(ctx context.Context, versionID, questionID int64, answer string, actor int64) (string, bool, error) {
	if s.clarificationService == nil {
		return "", false, errors.New("version: clarification service is not configured")
	}
	answered, err := s.clarificationService.AnswerQuestion(ctx, versionID, questionID, answer, actor, domain.AnsweredViaAgent)
	if err != nil {
		return toolOutcome(err)
	}
	return toolJSON(map[string]string{
		"questionId": formatID(answered.ID),
		"status":     answered.Status,
	})
}

// DecisionPropose records one proposed decision authored by the agent.
func (s *AgentToolService) DecisionPropose(ctx context.Context, versionID int64, requirementID, questionID *int64, topic, decision, rationale string, actor int64) (string, bool, error) {
	if s.clarificationService == nil {
		return "", false, errors.New("version: clarification service is not configured")
	}
	created, err := s.clarificationService.CreateDecision(ctx, versionID, DecisionInput{
		RequirementID: requirementID,
		QuestionID:    questionID,
		Topic:         topic,
		Decision:      decision,
		Rationale:     rationale,
	}, actor, domain.SourceAI)
	if err != nil {
		return toolOutcome(err)
	}
	return toolJSON(map[string]string{"decisionId": formatID(created.ID)})
}

// RelationPropose records one AI relation suggestion. A duplicate of an
// existing relation (including an undirected duplicate in the other direction)
// returns the existing id instead of failing (design spec §6.4/§8.3).
func (s *AgentToolService) RelationPropose(ctx context.Context, versionID, fromRequirementID, toRequirementID int64, relationType, description string, actor int64) (string, bool, error) {
	if s.relationService == nil {
		return "", false, errors.New("version: relation service is not configured")
	}
	relation, err := s.relationService.Create(ctx, versionID, CreateRelationCommand{
		FromRequirementID: fromRequirementID,
		ToRequirementID:   toRequirementID,
		RelationType:      relationType,
		Description:       description,
		Source:            domain.SourceAI,
	}, actor)
	if err != nil {
		var domainErr *domain.Error
		if errors.As(err, &domainErr) && domainErr.Reason == "duplicate_relation" {
			existing, listErr := s.contextRelations(ctx, versionID)
			if listErr != nil {
				return toolOutcome(listErr)
			}
			candidate := Relation{FromRequirementID: fromRequirementID, ToRequirementID: toRequirementID, RelationType: relationType}
			key := relationUniqueKey(&candidate)
			for i := range existing {
				if relationUniqueKey(&existing[i]) == key {
					return toolJSON(map[string]string{"relationId": formatID(existing[i].ID)})
				}
			}
		}
		return toolOutcome(err)
	}
	return toolJSON(map[string]string{"relationId": formatID(relation.ID)})
}

// AttachmentList returns the attachment metadata of one requirement or of the
// whole version. Storage paths and bytes are never exposed (design spec §8.3).
func (s *AgentToolService) AttachmentList(ctx context.Context, versionID int64, requirementID *int64) (string, bool, error) {
	if s.attachmentService == nil {
		return "", false, errors.New("version: attachment service is not configured")
	}
	var attachments []Attachment
	truncated := false
	if requirementID != nil {
		listed, err := s.attachmentService.List(ctx, versionID, *requirementID)
		if err != nil {
			return toolOutcome(err)
		}
		attachments = listed
	} else {
		requirements, more, err := s.contextRequirements(ctx, versionID)
		if err != nil {
			return toolOutcome(err)
		}
		truncated = more
		for i := range requirements {
			if len(attachments) >= contextAttachmentLimit {
				truncated = true
				break
			}
			listed, err := s.attachmentService.List(ctx, versionID, requirements[i].ID)
			if err != nil {
				return toolOutcome(err)
			}
			attachments = append(attachments, listed...)
		}
	}
	if len(attachments) > contextAttachmentLimit {
		attachments = attachments[:contextAttachmentLimit]
		truncated = true
	}
	views := make([]toolAttachmentView, 0, len(attachments))
	for i := range attachments {
		views = append(views, attachmentToolView(&attachments[i]))
	}
	return toolJSON(map[string]any{"attachments": views, "truncated": truncated})
}

// AttachmentReadText returns bounded UTF-8 text content of one text attachment.
func (s *AgentToolService) AttachmentReadText(ctx context.Context, versionID, attachmentID int64, maxBytes int) (string, bool, error) {
	if s.attachmentService == nil {
		return "", false, errors.New("version: attachment service is not configured")
	}
	attachment, err := s.attachmentService.Get(ctx, versionID, 0, attachmentID)
	if err != nil {
		return toolOutcome(err)
	}
	if attachment.Kind != domain.AttachmentText {
		return toolOutcome(domain.NewInvalid("file", "unsupported_attachment_type",
			fmt.Sprintf("attachment %d is not a text attachment", attachmentID)))
	}
	limit := maxBytes
	if limit <= 0 || limit > domain.MaxInjectedTextBytesPerFile {
		limit = domain.MaxInjectedTextBytesPerFile
	}
	content, err := s.attachmentService.ReadText(ctx, versionID, attachmentID, limit)
	if err != nil {
		return toolOutcome(err)
	}
	return toolJSON(map[string]any{
		"filename":  attachment.Filename,
		"content":   content,
		"truncated": attachment.SizeBytes > int64(limit),
	})
}

// ContextSummaryUpdate writes the active clarification session's summary
// (bounded to 20 KiB by the clarification service).
func (s *AgentToolService) ContextSummaryUpdate(ctx context.Context, versionID int64, summary string, actor int64) (string, bool, error) {
	if s.clarificationService == nil {
		return "", false, errors.New("version: clarification service is not configured")
	}
	if err := s.clarificationService.UpdateContextSummary(ctx, versionID, summary, actor); err != nil {
		return toolOutcome(err)
	}
	return toolJSON(map[string]bool{"ok": true})
}

// CodeTree lists the version's baseline code tree, optionally below one path.
func (s *AgentToolService) CodeTree(ctx context.Context, versionID int64, codePath string, maxEntries int) (string, bool, error) {
	repository, commit, err := s.codeBinding(ctx, versionID)
	if err != nil {
		return toolOutcome(err)
	}
	if err := validateToolCodePath(codePath, true); err != nil {
		return toolOutcome(err)
	}
	if s.git == nil {
		return "", false, errors.New("version: git reader is not configured")
	}
	limit := maxEntries
	if limit <= 0 || limit > maxToolCodeTreeEntries {
		limit = maxToolCodeTreeEntries
	}
	entries, err := s.git.Tree(ctx, repository.LocalPath, commit,
		repository.IncludeGlobs, repository.ExcludeGlobs, maxToolCodeTreeEntries)
	if err != nil {
		return toolOutcome(err)
	}
	prefix := strings.Trim(strings.TrimSpace(codePath), "/")
	filtered := make([]toolCodeEntryView, 0, len(entries))
	for _, entry := range entries {
		if prefix != "" && !strings.HasPrefix(entry.Path, prefix+"/") {
			continue
		}
		filtered = append(filtered, toolCodeEntryView{Path: entry.Path, Type: entry.Type, Size: entry.Size})
	}
	truncated := len(filtered) > limit
	if truncated {
		filtered = filtered[:limit]
	}
	return toolJSON(map[string]any{"entries": filtered, "truncated": truncated})
}

// CodeRead reads a bounded window of one text blob at the version's baseline
// commit (design spec §8.3 code.read).
func (s *AgentToolService) CodeRead(ctx context.Context, versionID int64, codePath string, offset, maxBytes int) (string, bool, error) {
	repository, commit, err := s.codeBinding(ctx, versionID)
	if err != nil {
		return toolOutcome(err)
	}
	if err := validateToolCodePath(codePath, false); err != nil {
		return toolOutcome(err)
	}
	if offset < 0 || offset > maxToolCodeReadOffset {
		return toolOutcome(domain.NewInvalid("offset", "invalid_offset",
			fmt.Sprintf("offset must be between 0 and %d", maxToolCodeReadOffset)))
	}
	if s.git == nil {
		return "", false, errors.New("version: git reader is not configured")
	}
	limit := maxBytes
	if limit <= 0 || limit > maxToolCodeReadBytes {
		limit = maxToolCodeReadBytes
	}

	content := ""
	var size int64
	if rangeReader, ok := s.git.(codeRangeReader); ok {
		content, size, err = rangeReader.ReadRange(ctx, repository.LocalPath, commit, codePath, offset, limit)
	} else {
		// Fallback for a GitReader without the optional range extension: read the
		// prefix and drop the offset locally. Only offsets inside the first read
		// window are honoured, and truncation cannot be detected.
		var raw string
		raw, err = s.git.Read(ctx, repository.LocalPath, commit, codePath, limit)
		if offset < len(raw) {
			content = raw[offset:]
		}
	}
	if err != nil {
		return toolOutcome(err)
	}
	if !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return toolOutcome(domain.NewInvalid("path", "unsupported_content",
			fmt.Sprintf("code path %q is not a text file", codePath)))
	}
	truncated := size > 0 && size > int64(offset)+int64(len(content))
	return toolJSON(map[string]any{
		"path":      codePath,
		"content":   content,
		"truncated": truncated,
	})
}

// codeRangeReader is the optional GitReader extension that reports the blob
// size alongside a window; the production gitread adapter implements it.
type codeRangeReader interface {
	ReadRange(ctx context.Context, repoPath, commit, path string, offset, maxBytes int) (string, int64, error)
}

// codeBinding resolves the version's bound repository and baseline commit.
func (s *AgentToolService) codeBinding(ctx context.Context, versionID int64) (CodeRepoRef, string, error) {
	if s.versions == nil {
		return CodeRepoRef{}, "", errors.New("version: agent tool service is not configured")
	}
	version, err := s.versions.Get(ctx, versionID)
	if err != nil {
		return CodeRepoRef{}, "", err
	}
	if version.CodeRepositoryID == nil || version.CodeBaselineCommit == nil {
		return CodeRepoRef{}, "", domain.NewInvalid("codeRepositoryId", "resource_unavailable",
			fmt.Sprintf("version %d has no code repository binding", versionID))
	}
	if s.resources == nil {
		return CodeRepoRef{}, "", errors.New("version: resource catalog is not configured")
	}
	repository, err := s.resources.GetCodeRepository(ctx, *version.CodeRepositoryID)
	if err != nil {
		return CodeRepoRef{}, "", err
	}
	if !repository.Available || (repository.Status != "" && repository.Status != resourceStatusActive) {
		return CodeRepoRef{}, "", domain.NewInvalid("codeRepositoryId", "resource_unavailable",
			fmt.Sprintf("code repository %d is not available", repository.ID))
	}
	return repository, *version.CodeBaselineCommit, nil
}

// contextRequirements loads at most contextRequirementLimit requirements of the
// version, reporting whether more exist.
func (s *AgentToolService) contextRequirements(ctx context.Context, versionID int64) ([]Requirement, bool, error) {
	if s.requirements == nil {
		return nil, false, nil
	}
	requirements := make([]Requirement, 0, contextRequirementPageSize)
	cursor := ""
	for len(requirements) < contextRequirementLimit {
		limit := contextRequirementPageSize
		if remaining := contextRequirementLimit - len(requirements); remaining < limit {
			limit = remaining
		}
		page, more, err := s.requirements.List(ctx, versionID, "", domain.Page{Limit: limit, Cursor: cursor})
		if err != nil {
			return nil, false, err
		}
		requirements = append(requirements, page...)
		if !more || len(page) == 0 {
			return requirements, false, nil
		}
		cursor = platform.EncodeCursor(page[len(page)-1].ID)
	}
	return requirements, true, nil
}

// contextRelations returns the version's relations, bounded by the relation
// service's 500-edge cap.
func (s *AgentToolService) contextRelations(ctx context.Context, versionID int64) ([]Relation, error) {
	if s.relationService != nil {
		return s.relationService.List(ctx, versionID)
	}
	if s.requirements == nil {
		return nil, nil
	}
	return s.requirements.ListRelations(ctx, versionID)
}

// contextQuestions loads the version's open and answered questions, bounded per
// status by contextQuestionLimit.
func (s *AgentToolService) contextQuestions(ctx context.Context, versionID int64) ([]Question, bool, error) {
	if s.clarifications == nil {
		return nil, false, nil
	}
	questions := make([]Question, 0, contextQuestionLimit)
	truncated := false
	for _, status := range []string{domain.QuestionOpen, domain.QuestionAnswered} {
		remaining := contextQuestionLimit - len(questions)
		if remaining <= 0 {
			truncated = true
			break
		}
		page, more, err := s.contextQuestionsPage(ctx, versionID, status, remaining)
		if err != nil {
			return nil, false, err
		}
		questions = append(questions, page...)
		if more {
			truncated = true
		}
	}
	return questions, truncated, nil
}

// contextQuestionsPage pages through one question status.
func (s *AgentToolService) contextQuestionsPage(ctx context.Context, versionID int64, status string, max int) ([]Question, bool, error) {
	questions := make([]Question, 0, max)
	cursor := ""
	for len(questions) < max {
		limit := max - len(questions)
		page, more, err := s.clarifications.ListQuestions(ctx, QuestionFilter{
			VersionID: versionID, Status: status,
			Page: domain.Page{Limit: limit, Cursor: cursor},
		})
		if err != nil {
			return nil, false, err
		}
		questions = append(questions, page...)
		if !more || len(page) == 0 {
			return questions, false, nil
		}
		cursor = platform.EncodeCursor(page[len(page)-1].ID)
	}
	return questions, true, nil
}

// contextDecisions loads the version's proposed and accepted decisions.
func (s *AgentToolService) contextDecisions(ctx context.Context, versionID int64) ([]Decision, bool, error) {
	if s.clarifications == nil {
		return nil, false, nil
	}
	decisions := make([]Decision, 0, contextDecisionLimit)
	truncated := false
	for _, status := range []string{domain.DecisionProposed, domain.DecisionAccepted} {
		remaining := contextDecisionLimit - len(decisions)
		if remaining <= 0 {
			truncated = true
			break
		}
		page, more, err := s.contextDecisionsPage(ctx, versionID, status, remaining)
		if err != nil {
			return nil, false, err
		}
		decisions = append(decisions, page...)
		if more {
			truncated = true
		}
	}
	return decisions, truncated, nil
}

// contextDecisionsPage pages through one decision status.
func (s *AgentToolService) contextDecisionsPage(ctx context.Context, versionID int64, status string, max int) ([]Decision, bool, error) {
	decisions := make([]Decision, 0, max)
	cursor := ""
	for len(decisions) < max {
		limit := max - len(decisions)
		page, more, err := s.clarifications.ListDecisions(ctx, DecisionFilter{
			VersionID: versionID, Status: status,
			Page: domain.Page{Limit: limit, Cursor: cursor},
		})
		if err != nil {
			return nil, false, err
		}
		decisions = append(decisions, page...)
		if !more || len(page) == 0 {
			return decisions, false, nil
		}
		cursor = platform.EncodeCursor(page[len(page)-1].ID)
	}
	return decisions, true, nil
}

// contextAttachments collects the attachment metadata of the listed
// requirements, bounded by contextAttachmentLimit.
func (s *AgentToolService) contextAttachments(ctx context.Context, versionID int64) ([]toolAttachmentView, bool, error) {
	if s.requirements == nil {
		return []toolAttachmentView{}, false, nil
	}
	requirements, requirementsMore, err := s.contextRequirements(ctx, versionID)
	if err != nil {
		return nil, false, err
	}
	views := make([]toolAttachmentView, 0)
	for i := range requirements {
		if len(views) >= contextAttachmentLimit {
			return views, true, nil
		}
		attachments, err := s.requirements.ListAttachments(ctx, requirements[i].ID)
		if err != nil {
			return nil, false, err
		}
		for j := range attachments {
			if len(views) >= contextAttachmentLimit {
				return views, true, nil
			}
			views = append(views, attachmentToolView(&attachments[j]))
		}
	}
	return views, requirementsMore, nil
}

// fitToolContext marshals the context and enforces the 32 KiB bound. Long
// fields are shortened and items dropped deterministically; the result is
// always valid JSON and carries truncated=true when anything was removed.
func fitToolContext(view toolContextView) (string, error) {
	encoded, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("version: encode tool context: %w", err)
	}
	if len(encoded) <= maxToolContextBytes {
		return string(encoded), nil
	}
	view.Truncated = true
	truncateToolContextStrings(&view)
	encoded, err = json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("version: encode tool context: %w", err)
	}
	if len(encoded) <= maxToolContextBytes {
		return string(encoded), nil
	}
	for dropToolContextSection(&view) {
		encoded, err = json.Marshal(view)
		if err != nil {
			return "", fmt.Errorf("version: encode tool context: %w", err)
		}
		if len(encoded) <= maxToolContextBytes {
			return string(encoded), nil
		}
	}
	// The minimal envelope always fits: clear the remaining long fields.
	view.Version.Description = ""
	view.Version.Label = ""
	view.ContextSummary = ""
	encoded, err = json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("version: encode tool context: %w", err)
	}
	if len(encoded) > maxToolContextBytes {
		return "", errors.New("version: tool context exceeds its budget")
	}
	return string(encoded), nil
}

// truncateToolContextStrings shortens the free-form fields of an oversized
// context before any item is dropped.
func truncateToolContextStrings(view *toolContextView) {
	view.Version.Description = truncateRunes(view.Version.Description, contextStringTrimRunes)
	view.Version.Label = truncateRunes(view.Version.Label, 100)
	view.ContextSummary = truncateRunes(view.ContextSummary, contextSummaryTrimRunes)
	for i := range view.Requirements {
		view.Requirements[i].Description = truncateRunes(view.Requirements[i].Description, contextStringTrimRunes)
		view.Requirements[i].AcceptanceCriteria = truncateRunes(view.Requirements[i].AcceptanceCriteria, contextStringTrimRunes)
	}
	for i := range view.Questions {
		view.Questions[i].Question = truncateRunes(view.Questions[i].Question, contextStringTrimRunes)
		view.Questions[i].Answer = truncateRunes(view.Questions[i].Answer, contextStringTrimRunes)
	}
	for i := range view.Decisions {
		view.Decisions[i].Decision = truncateRunes(view.Decisions[i].Decision, contextStringTrimRunes)
		view.Decisions[i].Rationale = truncateRunes(view.Decisions[i].Rationale, contextStringTrimRunes)
	}
}

// dropToolContextSection drops the last item of the largest remaining
// collection and reports whether anything was dropped.
func dropToolContextSection(view *toolContextView) bool {
	type section struct {
		length int
		drop   func()
	}
	sections := []section{
		{len(view.Requirements), func() { view.Requirements = view.Requirements[:len(view.Requirements)-1] }},
		{len(view.Relations), func() { view.Relations = view.Relations[:len(view.Relations)-1] }},
		{len(view.Questions), func() { view.Questions = view.Questions[:len(view.Questions)-1] }},
		{len(view.Decisions), func() { view.Decisions = view.Decisions[:len(view.Decisions)-1] }},
		{len(view.Attachments), func() { view.Attachments = view.Attachments[:len(view.Attachments)-1] }},
	}
	largest := -1
	for i := range sections {
		if sections[i].length > 0 && (largest < 0 || sections[i].length > sections[largest].length) {
			largest = i
		}
	}
	if largest < 0 {
		return false
	}
	sections[largest].drop()
	return true
}

// decodeDraftPatch validates the requirement.update_draft patch. Only the four
// draft fields are accepted (design spec §8.3/§8.7).
func decodeDraftPatch(revision int, raw json.RawMessage) (UpdateRequirementCommand, error) {
	invalid := func() error {
		return domain.NewInvalid("patch", "invalid_patch",
			"patch must be an object with title, description, acceptanceCriteria or priority")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return UpdateRequirementCommand{}, invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var patch struct {
		Title              *string `json:"title"`
		Description        *string `json:"description"`
		AcceptanceCriteria *string `json:"acceptanceCriteria"`
		Priority           *string `json:"priority"`
	}
	if err := decoder.Decode(&patch); err != nil {
		return UpdateRequirementCommand{}, invalid()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return UpdateRequirementCommand{}, invalid()
	}
	if patch.Title == nil && patch.Description == nil && patch.AcceptanceCriteria == nil && patch.Priority == nil {
		return UpdateRequirementCommand{}, domain.NewInvalid("patch", "invalid_patch",
			"patch must change at least one of title, description, acceptanceCriteria or priority")
	}
	return UpdateRequirementCommand{
		Revision:           revision,
		Title:              patch.Title,
		Description:        patch.Description,
		AcceptanceCriteria: patch.AcceptanceCriteria,
		Priority:           patch.Priority,
		Source:             domain.SourceAI,
	}, nil
}

// validateToolCodePath rejects absolute paths, Windows separators, NUL bytes
// and any ".." component. tree allows the empty root path; read requires a
// file.
func validateToolCodePath(raw string, allowEmpty bool) error {
	invalid := func() error {
		return domain.NewInvalid("path", "invalid_path",
			fmt.Sprintf("code path %q is not a safe repository-relative path", raw))
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if allowEmpty {
			return nil
		}
		return domain.NewInvalid("path", "invalid_path", "path is required")
	}
	if strings.ContainsAny(trimmed, "\x00\\") || strings.HasPrefix(trimmed, "/") ||
		filepath.IsAbs(trimmed) || filepath.VolumeName(trimmed) != "" {
		return invalid()
	}
	for _, component := range strings.Split(trimmed, "/") {
		if component == ".." {
			return invalid()
		}
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return invalid()
	}
	return nil
}

// toolOutcome classifies a service/port error: typed domain, resource and git
// errors are model-correctable business failures; everything else is an
// infrastructure failure the P1 executor reports as a failed tool call.
func toolOutcome(err error) (string, bool, error) {
	if err == nil {
		return "", false, nil
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		return businessContent(domainErr.Reason, err.Error()), true, nil
	}
	var resourceErr *ResourceError
	if errors.As(err, &resourceErr) {
		return businessContent(resourceErr.Reason, err.Error()), true, nil
	}
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		return businessContent(gitErr.Reason, err.Error()), true, nil
	}
	return "", false, err
}

// businessContent prefixes the stable spec reason so the model gets an
// actionable, correctable message even though Go error messages are free-form.
func businessContent(reason, message string) string {
	if reason == "" || strings.Contains(message, reason) {
		return message
	}
	return reason + ": " + message
}

// toolJSON marshals one successful tool payload.
func toolJSON(payload any) (string, bool, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false, fmt.Errorf("version: encode agent tool result: %w", err)
	}
	return string(encoded), false, nil
}

// formatID renders a P3-owned id as the decimal string the tool contract uses
// (design spec §7.1/§8.3).
func formatID(id int64) string {
	return strconv.FormatInt(id, 10)
}

// optionalID renders an optional P3-owned id as a decimal string pointer.
func optionalID(id *int64) *string {
	if id == nil {
		return nil
	}
	value := formatID(*id)
	return &value
}

// stringValue dereferences an optional string, mapping nil to "".
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// truncateRunes bounds s to at most max runes.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// toolVersionCounts is the aggregate counters section of version.context_get.
type toolVersionCounts struct {
	RequirementCount  int `json:"requirementCount"`
	ClarifiedCount    int `json:"clarifiedCount"`
	OpenQuestionCount int `json:"openQuestionCount"`
}

// toolVersionView is the version projection of version.context_get.
type toolVersionView struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Label              string            `json:"label,omitempty"`
	Description        string            `json:"description,omitempty"`
	Status             string            `json:"status"`
	Revision           int               `json:"revision"`
	SystemID           *int64            `json:"systemId,omitempty"`
	EnvironmentID      *int64            `json:"environmentId,omitempty"`
	CodeRepositoryID   *int64            `json:"codeRepositoryId,omitempty"`
	CodeBranch         string            `json:"codeBranch,omitempty"`
	CodeBaselineCommit string            `json:"codeBaselineCommit,omitempty"`
	Counts             toolVersionCounts `json:"counts"`
}

// toolRequirementView is one requirement of version.context_get.
type toolRequirementView struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	Status             string `json:"status"`
	Description        string `json:"description,omitempty"`
	AcceptanceCriteria string `json:"acceptanceCriteria,omitempty"`
	Priority           string `json:"priority"`
	Revision           int    `json:"revision"`
	OpenQuestionCount  int    `json:"openQuestionCount"`
}

// toolRequirementDetailView is the requirement.update_draft result.
type toolRequirementDetailView struct {
	ID                 string `json:"id"`
	VersionID          string `json:"versionId"`
	Title              string `json:"title"`
	Status             string `json:"status"`
	Description        string `json:"description,omitempty"`
	AcceptanceCriteria string `json:"acceptanceCriteria,omitempty"`
	Priority           string `json:"priority"`
	Revision           int    `json:"revision"`
	Source             string `json:"source"`
	LastUpdateSource   string `json:"lastUpdateSource"`
}

// toolRelationView is one relation of version.context_get.
type toolRelationView struct {
	ID                string `json:"id"`
	FromRequirementID string `json:"fromRequirementId"`
	ToRequirementID   string `json:"toRequirementId"`
	RelationType      string `json:"relationType"`
	Description       string `json:"description,omitempty"`
}

// toolQuestionView is one open/answered question of version.context_get.
type toolQuestionView struct {
	ID            string  `json:"id"`
	RequirementID *string `json:"requirementId,omitempty"`
	Question      string  `json:"question"`
	Status        string  `json:"status"`
	Answer        string  `json:"answer,omitempty"`
	Source        string  `json:"source"`
}

// toolDecisionView is one proposed/accepted decision of version.context_get.
type toolDecisionView struct {
	ID            string  `json:"id"`
	RequirementID *string `json:"requirementId,omitempty"`
	QuestionID    *string `json:"questionId,omitempty"`
	Topic         string  `json:"topic"`
	Decision      string  `json:"decision"`
	Rationale     string  `json:"rationale,omitempty"`
	Status        string  `json:"status"`
}

// toolAttachmentView is the attachment metadata exposed to the agent. It never
// carries storage_path, bytes or text.
type toolAttachmentView struct {
	ID            string `json:"id"`
	RequirementID string `json:"requirementId"`
	Filename      string `json:"filename"`
	MIMEType      string `json:"mimeType"`
	Kind          string `json:"kind"`
	SizeBytes     int64  `json:"sizeBytes"`
	SHA256        string `json:"sha256"`
	CreatedAt     string `json:"createdAt"`
}

// attachmentToolView projects one attachment onto the tool metadata view.
func attachmentToolView(attachment *Attachment) toolAttachmentView {
	return toolAttachmentView{
		ID:            formatID(attachment.ID),
		RequirementID: formatID(attachment.RequirementID),
		Filename:      attachment.Filename,
		MIMEType:      attachment.MIMEType,
		Kind:          attachment.Kind,
		SizeBytes:     attachment.SizeBytes,
		SHA256:        attachment.SHA256,
		CreatedAt:     attachment.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// toolCodeEntryView is one code.tree entry.
type toolCodeEntryView struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// toolContextView is the version.context_get payload.
type toolContextView struct {
	Version        toolVersionView       `json:"version"`
	Requirements   []toolRequirementView `json:"requirements"`
	Relations      []toolRelationView    `json:"relations"`
	Questions      []toolQuestionView    `json:"questions"`
	Decisions      []toolDecisionView    `json:"decisions"`
	Attachments    []toolAttachmentView  `json:"attachments"`
	ContextSummary string                `json:"contextSummary,omitempty"`
	Truncated      bool                  `json:"truncated"`
}
