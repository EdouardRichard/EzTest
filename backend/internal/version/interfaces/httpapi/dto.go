package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// versionResourceDTO is one resolved P2 binding target. Its id stays a number
// (P2 owns it); available is false when the resource was deleted after binding.
type versionResourceDTO struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

// versionBindingsDTO is the resolved binding view; unbound entries are null.
type versionBindingsDTO struct {
	System         *versionResourceDTO `json:"system"`
	Environment    *versionResourceDTO `json:"environment"`
	CodeRepository *versionResourceDTO `json:"codeRepository"`
}

// versionDTO is the VersionDTO of spec §7.2. The P3-owned id is a decimal JSON
// string; the P2 resource ids and the user ids stay numbers.
type versionDTO struct {
	ID          int64  `json:"id,string"`
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Status      string `json:"status"`
	// Paused, PauseMode and BatchNo are the P6 execution-state additions
	// (execution spec §6.3). The active batch itself is deliberately not part
	// of the version DTO: the frontend reads it from
	// GET /api/versions/{id}/batches?status=running&limit=1.
	Paused             bool               `json:"paused"`
	PauseMode          string             `json:"pause_mode"`
	BatchNo            int                `json:"batch_no"`
	SystemID           *int64             `json:"systemId"`
	EnvironmentID      *int64             `json:"environmentId"`
	CodeRepositoryID   *int64             `json:"codeRepositoryId"`
	CodeBranch         *string            `json:"codeBranch"`
	CodeBaselineCommit *string            `json:"codeBaselineCommit"`
	Bindings           versionBindingsDTO `json:"bindings"`
	RequirementCount   int                `json:"requirementCount"`
	ClarifiedCount     int                `json:"clarifiedCount"`
	OpenQuestionCount  int                `json:"openQuestionCount"`
	Revision           int                `json:"revision"`
	CreatedBy          int64              `json:"createdBy"`
	UpdatedBy          int64              `json:"updatedBy"`
	CreatedAt          time.Time          `json:"createdAt"`
	UpdatedAt          time.Time          `json:"updatedAt"`
}

// newVersionDTO maps a version aggregate, its aggregate counters and its
// resolved bindings to the API shape.
func newVersionDTO(version *versionapp.Version, counts versionCountsView, bindings versionBindingsDTO) versionDTO {
	return versionDTO{
		ID:                 version.ID,
		Name:               version.Name,
		Label:              version.Label,
		Description:        version.Description,
		Status:             version.Status,
		Paused:             version.Paused,
		PauseMode:          version.PauseMode,
		BatchNo:            version.BatchNo,
		SystemID:           version.SystemID,
		EnvironmentID:      version.EnvironmentID,
		CodeRepositoryID:   version.CodeRepositoryID,
		CodeBranch:         version.CodeBranch,
		CodeBaselineCommit: version.CodeBaselineCommit,
		Bindings:           bindings,
		RequirementCount:   counts.requirementCount,
		ClarifiedCount:     counts.clarifiedCount,
		OpenQuestionCount:  counts.openQuestionCount,
		Revision:           version.Revision,
		CreatedBy:          version.CreatedBy,
		UpdatedBy:          version.UpdatedBy,
		CreatedAt:          version.CreatedAt,
		UpdatedAt:          version.UpdatedAt,
	}
}

// versionCountsView is the handler-local projection of the version counters so
// dto.go does not depend on the domain package for one struct.
type versionCountsView struct {
	requirementCount  int
	clarifiedCount    int
	openQuestionCount int
}

// createVersionRequest is the POST /api/versions body (spec §7.2). P2 resource
// ids are numbers.
type createVersionRequest struct {
	Name               string  `json:"name"`
	Label              string  `json:"label"`
	Description        string  `json:"description"`
	SystemID           *int64  `json:"systemId"`
	EnvironmentID      *int64  `json:"environmentId"`
	CodeRepositoryID   *int64  `json:"codeRepositoryId"`
	CodeBranch         *string `json:"codeBranch"`
	CodeBaselineCommit *string `json:"codeBaselineCommit"`
}

// updateVersionRequest is the PATCH /api/versions/{id} body: revision is
// required, the metadata fields are optional and keep their stored value when
// omitted.
type updateVersionRequest struct {
	Revision    int     `json:"revision"`
	Name        *string `json:"name"`
	Label       *string `json:"label"`
	Description *string `json:"description"`
}

// putVersionBindingsRequest replaces the whole binding set; an omitted or null
// field clears that binding.
type putVersionBindingsRequest struct {
	Revision           int     `json:"revision"`
	SystemID           *int64  `json:"systemId"`
	EnvironmentID      *int64  `json:"environmentId"`
	CodeRepositoryID   *int64  `json:"codeRepositoryId"`
	CodeBranch         *string `json:"codeBranch"`
	CodeBaselineCommit *string `json:"codeBaselineCommit"`
}

// enterDesignRequest is the optional POST enter-design body.
type enterDesignRequest struct {
	AcknowledgeUnclarified bool `json:"acknowledgeUnclarified"`
}

// enterDesignResultDTO is the enter-design success body (spec §7.2): the
// version plus the flattened human-readable warnings. Warnings is always an
// array, empty when nothing is left to clarify.
type enterDesignResultDTO struct {
	Version  versionDTO `json:"version"`
	Warnings []string   `json:"warnings"`
}

// warningMessages flattens the structured enter-design warnings into the string
// array the frontend renders (lead ruling 2), for example
// "3 条需求未澄清 / 2 个待澄清问题".
func warningMessages(warnings versionapp.Warnings) []string {
	clauses := make([]string, 0, 2)
	if count := len(warnings.UnclarifiedRequirementIDs); count > 0 {
		clauses = append(clauses, fmt.Sprintf("%d 条需求未澄清", count))
	}
	if warnings.OpenQuestionCount > 0 {
		clauses = append(clauses, fmt.Sprintf("%d 个待澄清问题", warnings.OpenQuestionCount))
	}
	if len(clauses) == 0 {
		return []string{}
	}
	return []string{strings.Join(clauses, " / ")}
}

// requirementDTO is the RequirementDTO of spec §7.3. Every P3-owned id is a
// decimal JSON string; user ids stay numbers.
type requirementDTO struct {
	ID                 int64            `json:"id,string"`
	VersionID          int64            `json:"versionId,string"`
	Title              string           `json:"title"`
	Description        string           `json:"description"`
	AcceptanceCriteria string           `json:"acceptanceCriteria"`
	Priority           string           `json:"priority"`
	Status             string           `json:"status"`
	Source             string           `json:"source"`
	LastUpdateSource   string           `json:"lastUpdateSource"`
	Revision           int              `json:"revision"`
	ConfirmedAt        *time.Time       `json:"confirmedAt"`
	ConfirmedBy        *int64           `json:"confirmedBy"`
	OpenQuestionCount  int              `json:"openQuestionCount"`
	RelationCount      int              `json:"relationCount"`
	AttachmentCount    int              `json:"attachmentCount"`
	Attachments        *[]attachmentDTO `json:"attachments,omitempty"`
	CreatedBy          int64            `json:"createdBy"`
	UpdatedBy          int64            `json:"updatedBy"`
	CreatedAt          time.Time        `json:"createdAt"`
	UpdatedAt          time.Time        `json:"updatedAt"`
}

// newRequirementDTO maps a requirement aggregate and its counters to the API
// shape. attachments stays nil (omitted) on list/create/action responses and is
// set to a non-nil array on the requirement detail.
func newRequirementDTO(requirement *versionapp.Requirement, openQuestionCount, relationCount, attachmentCount int, attachments []attachmentDTO, includeAttachments bool) requirementDTO {
	dto := requirementDTO{
		ID:                 requirement.ID,
		VersionID:          requirement.VersionID,
		Title:              requirement.Title,
		Description:        requirement.Description,
		AcceptanceCriteria: requirement.AcceptanceCriteria,
		Priority:           requirement.Priority,
		Status:             requirement.Status,
		Source:             requirement.Source,
		LastUpdateSource:   requirement.LastUpdateSource,
		Revision:           requirement.Revision,
		ConfirmedAt:        requirement.ConfirmedAt,
		ConfirmedBy:        requirement.ConfirmedBy,
		OpenQuestionCount:  openQuestionCount,
		RelationCount:      relationCount,
		AttachmentCount:    attachmentCount,
		CreatedBy:          requirement.CreatedBy,
		UpdatedBy:          requirement.UpdatedBy,
		CreatedAt:          requirement.CreatedAt,
		UpdatedAt:          requirement.UpdatedAt,
	}
	if includeAttachments {
		list := attachments
		if list == nil {
			list = []attachmentDTO{}
		}
		dto.Attachments = &list
	}
	return dto
}

// createRequirementRequest is the POST requirement body (spec §7.3).
type createRequirementRequest struct {
	Title              string `json:"title"`
	Description        string `json:"description"`
	AcceptanceCriteria string `json:"acceptanceCriteria"`
	Priority           string `json:"priority"`
}

// updateRequirementRequest is the PATCH requirement body: revision is required
// and every editable field is optional.
type updateRequirementRequest struct {
	Revision           int     `json:"revision"`
	Title              *string `json:"title"`
	Description        *string `json:"description"`
	AcceptanceCriteria *string `json:"acceptanceCriteria"`
	Priority           *string `json:"priority"`
}

// relationDTO is the RelationDTO of spec §7.4. Both endpoint ids are P3
// decimal strings.
type relationDTO struct {
	ID                int64     `json:"id,string"`
	VersionID         int64     `json:"versionId,string"`
	FromRequirementID int64     `json:"fromRequirementId,string"`
	ToRequirementID   int64     `json:"toRequirementId,string"`
	RelationType      string    `json:"relationType"`
	Description       string    `json:"description"`
	Source            string    `json:"source"`
	CreatedBy         int64     `json:"createdBy"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// newRelationDTO maps one relation aggregate to the API shape.
func newRelationDTO(relation *versionapp.Relation) relationDTO {
	return relationDTO{
		ID:                relation.ID,
		VersionID:         relation.VersionID,
		FromRequirementID: relation.FromRequirementID,
		ToRequirementID:   relation.ToRequirementID,
		RelationType:      relation.RelationType,
		Description:       relation.Description,
		Source:            relation.Source,
		CreatedBy:         relation.CreatedBy,
		CreatedAt:         relation.CreatedAt,
		UpdatedAt:         relation.UpdatedAt,
	}
}

// newRelationDTOs maps a relation slice to a non-nil DTO slice.
func newRelationDTOs(relations []versionapp.Relation) []relationDTO {
	items := make([]relationDTO, 0, len(relations))
	for i := range relations {
		items = append(items, newRelationDTO(&relations[i]))
	}
	return items
}

// createRelationRequest is the POST relation body; the endpoint ids are decimal
// strings, so they are decoded with the ,string JSON tag.
type createRelationRequest struct {
	FromRequirementID int64  `json:"fromRequirementId,string"`
	ToRequirementID   int64  `json:"toRequirementId,string"`
	RelationType      string `json:"relationType"`
	Description       string `json:"description"`
}

// updateRelationRequest is the relation PATCH body. Relations have no revision;
// pointer fields keep true PATCH semantics so an omitted field stays nil while
// an explicit empty relationType reaches the service and is rejected.
type updateRelationRequest struct {
	RelationType *string `json:"relationType"`
	Description  *string `json:"description"`
}

// attachmentDTO is the attachment metadata shape of spec §7.3: no storagePath
// is ever returned (the bytes stay behind the authenticated download route).
type attachmentDTO struct {
	ID        int64     `json:"id,string"`
	Kind      string    `json:"kind"`
	Filename  string    `json:"filename"`
	MIMEType  string    `json:"mimeType"`
	SizeBytes int64     `json:"sizeBytes"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"createdAt"`
}

// newAttachmentDTO maps one attachment aggregate to its metadata shape.
func newAttachmentDTO(attachment *versionapp.Attachment) attachmentDTO {
	return attachmentDTO{
		ID:        attachment.ID,
		Kind:      attachment.Kind,
		Filename:  attachment.Filename,
		MIMEType:  attachment.MIMEType,
		SizeBytes: attachment.SizeBytes,
		SHA256:    attachment.SHA256,
		CreatedAt: attachment.CreatedAt,
	}
}

// newAttachmentDTOs maps an attachment slice to a non-nil DTO slice.
func newAttachmentDTOs(attachments []versionapp.Attachment) []attachmentDTO {
	items := make([]attachmentDTO, 0, len(attachments))
	for i := range attachments {
		items = append(items, newAttachmentDTO(&attachments[i]))
	}
	return items
}

// clarificationSessionDTO is the ClarificationSession shape of spec §7.5. The
// P3 session id is a decimal string; the P1 agentSessionId stays a number.
type clarificationSessionDTO struct {
	ID               int64      `json:"id,string"`
	Status           string     `json:"status"`
	AgentSessionID   *int64     `json:"agentSessionId"`
	StartedAt        time.Time  `json:"startedAt"`
	EndedAt          *time.Time `json:"endedAt"`
	ContextSummary   string     `json:"contextSummary"`
	SummaryUpdatedAt *time.Time `json:"summaryUpdatedAt"`
}

// newClarificationSessionDTO maps one session aggregate to the API shape.
func newClarificationSessionDTO(session *versionapp.ClarificationSession) clarificationSessionDTO {
	return clarificationSessionDTO{
		ID:               session.ID,
		Status:           session.Status,
		AgentSessionID:   session.AgentSessionID,
		StartedAt:        session.StartedAt,
		EndedAt:          session.EndedAt,
		ContextSummary:   session.ContextSummary,
		SummaryUpdatedAt: session.SummaryUpdatedAt,
	}
}

// clarificationSessionStartedDTO is the narrow 201 body of the session start
// (spec §7.5): only the fields known before completion and context summary.
type clarificationSessionStartedDTO struct {
	ID             int64     `json:"id,string"`
	Status         string    `json:"status"`
	AgentSessionID *int64    `json:"agentSessionId"`
	StartedAt      time.Time `json:"startedAt"`
}

// newClarificationSessionStartedDTO maps a freshly started session.
func newClarificationSessionStartedDTO(session *versionapp.ClarificationSession) clarificationSessionStartedDTO {
	return clarificationSessionStartedDTO{
		ID:             session.ID,
		Status:         session.Status,
		AgentSessionID: session.AgentSessionID,
		StartedAt:      session.StartedAt,
	}
}

// clarificationQuestionCountsDTO is the per-status question counter block.
type clarificationQuestionCountsDTO struct {
	Open      int `json:"open"`
	Answered  int `json:"answered"`
	Dismissed int `json:"dismissed"`
}

// clarificationDecisionCountsDTO is the per-status decision counter block.
type clarificationDecisionCountsDTO struct {
	Proposed int `json:"proposed"`
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
}

// clarificationOverviewDTO is the GET clarification body (spec §7.5). Session is
// null before the first session is started.
type clarificationOverviewDTO struct {
	Session   *clarificationSessionDTO       `json:"session"`
	Questions clarificationQuestionCountsDTO `json:"questions"`
	Decisions clarificationDecisionCountsDTO `json:"decisions"`
}

// newClarificationOverviewDTO maps the active session (nil when none) and the
// per-status counters to the overview shape.
func newClarificationOverviewDTO(session *versionapp.ClarificationSession, counts versionapp.ClarificationCounts) clarificationOverviewDTO {
	dto := clarificationOverviewDTO{
		Questions: clarificationQuestionCountsDTO{
			Open:      counts.OpenQuestions,
			Answered:  counts.AnsweredQuestions,
			Dismissed: counts.DismissedQuestions,
		},
		Decisions: clarificationDecisionCountsDTO{
			Proposed: counts.ProposedDecisions,
			Accepted: counts.AcceptedDecisions,
			Rejected: counts.RejectedDecisions,
		},
	}
	if session != nil {
		item := newClarificationSessionDTO(session)
		dto.Session = &item
	}
	return dto
}

// clarificationMessagePartDTO is one persisted P1 message part. Image parts keep
// only their type: the stored image reference is not exposed by the chat read
// model (the data URL never leaves the DB/LLM boundary).
type clarificationMessagePartDTO struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// clarificationMessageDTO is the P1 message projection of spec §7.5. The P1
// message, run and agent session ids stay numbers; system messages are filtered
// out before this mapping.
type clarificationMessageDTO struct {
	ID        int64                         `json:"id"`
	Role      string                        `json:"role"`
	Parts     []clarificationMessagePartDTO `json:"parts"`
	Reasoning string                        `json:"reasoning,omitempty"`
	ToolCalls json.RawMessage               `json:"toolCalls,omitempty"`
	ToolName  string                        `json:"toolName,omitempty"`
	RunID     int64                         `json:"runId"`
	CreatedAt time.Time                     `json:"createdAt"`
}

// newClarificationMessageDTO maps one projected P1 message.
func newClarificationMessageDTO(message *versionapp.AgentMessage) clarificationMessageDTO {
	parts := make([]clarificationMessagePartDTO, 0, len(message.Parts))
	for _, part := range message.Parts {
		parts = append(parts, clarificationMessagePartDTO{Type: part.Type, Text: part.Text})
	}
	return clarificationMessageDTO{
		ID:        message.ID,
		Role:      message.Role,
		Parts:     parts,
		Reasoning: message.Reasoning,
		ToolCalls: message.ToolCalls,
		ToolName:  message.ToolName,
		RunID:     message.RunID,
		CreatedAt: message.CreatedAt,
	}
}

// newClarificationMessageDTOs maps a message slice to a non-nil DTO slice.
func newClarificationMessageDTOs(messages []versionapp.AgentMessage) []clarificationMessageDTO {
	items := make([]clarificationMessageDTO, 0, len(messages))
	for i := range messages {
		items = append(items, newClarificationMessageDTO(&messages[i]))
	}
	return items
}

// sendClarificationMessageResultDTO is the 202 body of the send-message action
// (spec §7.5): the P3 session id is a decimal string, the P1 ids are numbers.
type sendClarificationMessageResultDTO struct {
	SessionID      int64  `json:"sessionId,string"`
	AgentSessionID int64  `json:"agentSessionId"`
	RunID          int64  `json:"runId"`
	Status         string `json:"status"`
}

// newSendClarificationMessageResultDTO maps the started run result. The status
// is always "running" for a freshly started run (spec §7.5).
func newSendClarificationMessageResultDTO(result versionapp.StartRunResult) sendClarificationMessageResultDTO {
	status := result.Status
	if status == "" {
		status = "running"
	}
	return sendClarificationMessageResultDTO{
		SessionID:      result.SessionID,
		AgentSessionID: result.AgentSessionID,
		RunID:          result.RunID,
		Status:         status,
	}
}

// clarificationRunResultDTO is the 202 body of the one-shot analyze/recon runs
// (spec §7.5): only the P1 agent session and run ids, both numbers.
type clarificationRunResultDTO struct {
	AgentSessionID int64 `json:"agentSessionId"`
	RunID          int64 `json:"runId"`
}

// newClarificationRunResultDTO maps the started one-shot run result.
func newClarificationRunResultDTO(result versionapp.StartRunResult) clarificationRunResultDTO {
	return clarificationRunResultDTO{AgentSessionID: result.AgentSessionID, RunID: result.RunID}
}

// questionDTO is the QuestionDTO of spec §7.6. Every P3-owned id is a decimal
// string; user ids stay numbers.
type questionDTO struct {
	ID            int64      `json:"id,string"`
	VersionID     int64      `json:"versionId,string"`
	RequirementID *int64     `json:"requirementId,string"`
	SessionID     *int64     `json:"sessionId,string"`
	Question      string     `json:"question"`
	Status        string     `json:"status"`
	Answer        string     `json:"answer"`
	Source        string     `json:"source"`
	AnsweredVia   string     `json:"answeredVia"`
	AnsweredBy    *int64     `json:"answeredBy"`
	AnsweredAt    *time.Time `json:"answeredAt"`
	CreatedBy     int64      `json:"createdBy"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// newQuestionDTO maps one question aggregate to the API shape.
func newQuestionDTO(question *versionapp.Question) questionDTO {
	return questionDTO{
		ID:            question.ID,
		VersionID:     question.VersionID,
		RequirementID: question.RequirementID,
		SessionID:     question.SessionID,
		Question:      question.Question,
		Status:        question.Status,
		Answer:        question.Answer,
		Source:        question.Source,
		AnsweredVia:   question.AnsweredVia,
		AnsweredBy:    question.AnsweredBy,
		AnsweredAt:    question.AnsweredAt,
		CreatedBy:     question.CreatedBy,
		CreatedAt:     question.CreatedAt,
		UpdatedAt:     question.UpdatedAt,
	}
}

// newQuestionDTOs maps a question slice to a non-nil DTO slice.
func newQuestionDTOs(questions []versionapp.Question) []questionDTO {
	items := make([]questionDTO, 0, len(questions))
	for i := range questions {
		items = append(items, newQuestionDTO(&questions[i]))
	}
	return items
}

// decisionDTO is the DecisionDTO of spec §7.6. Every P3-owned id is a decimal
// string; user ids stay numbers.
type decisionDTO struct {
	ID            int64      `json:"id,string"`
	VersionID     int64      `json:"versionId,string"`
	RequirementID *int64     `json:"requirementId,string"`
	QuestionID    *int64     `json:"questionId,string"`
	Topic         string     `json:"topic"`
	Decision      string     `json:"decision"`
	Rationale     string     `json:"rationale"`
	Status        string     `json:"status"`
	Source        string     `json:"source"`
	ProposedBy    int64      `json:"proposedBy"`
	DecidedBy     *int64     `json:"decidedBy"`
	DecidedAt     *time.Time `json:"decidedAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// newDecisionDTO maps one decision aggregate to the API shape.
func newDecisionDTO(decision *versionapp.Decision) decisionDTO {
	return decisionDTO{
		ID:            decision.ID,
		VersionID:     decision.VersionID,
		RequirementID: decision.RequirementID,
		QuestionID:    decision.QuestionID,
		Topic:         decision.Topic,
		Decision:      decision.Decision,
		Rationale:     decision.Rationale,
		Status:        decision.Status,
		Source:        decision.Source,
		ProposedBy:    decision.ProposedBy,
		DecidedBy:     decision.DecidedBy,
		DecidedAt:     decision.DecidedAt,
		CreatedAt:     decision.CreatedAt,
		UpdatedAt:     decision.UpdatedAt,
	}
}

// newDecisionDTOs maps a decision slice to a non-nil DTO slice.
func newDecisionDTOs(decisions []versionapp.Decision) []decisionDTO {
	items := make([]decisionDTO, 0, len(decisions))
	for i := range decisions {
		items = append(items, newDecisionDTO(&decisions[i]))
	}
	return items
}

// versionEventRefsDTO is the frozen V1 refs block; absent arrays are [].
type versionEventRefsDTO struct {
	Evidence    []string `json:"evidence"`
	Case        []string `json:"case"`
	Requirement []string `json:"requirement"`
	Code        []string `json:"code"`
}

// versionEventDTO is the frozen V1 event envelope used by the version events
// snapshot and the version SSE stream (spec §7.7). It mirrors
// eventing/interfaces/httpapi's eventPayload field for field.
type versionEventDTO struct {
	Seq       int64               `json:"seq"`
	Channel   string              `json:"channel"`
	Type      string              `json:"type"`
	VersionID *string             `json:"version_id"`
	BatchID   *string             `json:"batch_id"`
	CaseID    *string             `json:"case_id"`
	StepID    *string             `json:"step_id"`
	AgentRole *string             `json:"agent_role"`
	SessionID *string             `json:"session_id"`
	TS        time.Time           `json:"ts"`
	Summary   string              `json:"summary"`
	Detail    json.RawMessage     `json:"detail"`
	Refs      versionEventRefsDTO `json:"refs"`
}

// newVersionEventDTO maps one persisted event to the V1 envelope. The detail
// falls back to {} when a row carries invalid or empty JSON, and the four refs
// arrays are always present.
func newVersionEventDTO(event *eventing.Event) versionEventDTO {
	detail := event.Detail
	if len(strings.TrimSpace(string(detail))) == 0 || !json.Valid(detail) {
		detail = json.RawMessage(`{}`)
	}
	return versionEventDTO{
		Seq:       event.Seq,
		Channel:   event.Channel,
		Type:      event.Type,
		VersionID: event.VersionID,
		BatchID:   event.BatchID,
		CaseID:    event.CaseID,
		StepID:    event.StepID,
		AgentRole: event.AgentRole,
		SessionID: event.SessionID,
		TS:        event.TS.UTC(),
		Summary:   event.Summary,
		Detail:    detail,
		Refs: versionEventRefsDTO{
			Evidence:    nonNilStrings(event.Refs.Evidence),
			Case:        nonNilStrings(event.Refs.Case),
			Requirement: nonNilStrings(event.Refs.Requirement),
			Code:        nonNilStrings(event.Refs.Code),
		},
	}
}

// newVersionEventDTOs maps an event slice to a non-nil DTO slice.
func newVersionEventDTOs(events []eventing.Event) []versionEventDTO {
	items := make([]versionEventDTO, 0, len(events))
	for i := range events {
		items = append(items, newVersionEventDTO(&events[i]))
	}
	return items
}

// nonNilStrings keeps the frozen V1 refs envelope: a missing array is [].
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// versionEventSnapshotEnvelope is the flat P3 events snapshot of spec §7.7,
// unlike the nested P0 snapshot: {data:[...], snapshot_cursor, has_more}.
type versionEventSnapshotEnvelope struct {
	Data           []versionEventDTO `json:"data"`
	SnapshotCursor int64             `json:"snapshot_cursor"`
	HasMore        bool              `json:"has_more"`
}

// sendClarificationMessageRequest is the POST clarification/messages body: the
// text is 1-5000 characters (the service enforces both bounds).
type sendClarificationMessageRequest struct {
	Message string `json:"message"`
}

// createQuestionRequest is the POST clarification/questions body. requirementId
// is an optional P3 decimal string; omitting it creates a version-level
// question.
type createQuestionRequest struct {
	Question      string `json:"question"`
	RequirementID *int64 `json:"requirementId,string"`
}

// updateQuestionRequest is the PATCH clarification/questions/{qid} body. Every
// field is optional; a status transition picks the service action.
type updateQuestionRequest struct {
	Status   *string `json:"status"`
	Question *string `json:"question"`
	Answer   *string `json:"answer"`
}

// createDecisionRequest is the POST decisions body. requirementId/questionId are
// optional P3 decimal strings. A human decision is stored as accepted.
type createDecisionRequest struct {
	RequirementID *int64 `json:"requirementId,string"`
	QuestionID    *int64 `json:"questionId,string"`
	Topic         string `json:"topic"`
	Decision      string `json:"decision"`
	Rationale     string `json:"rationale"`
}

// updateDecisionRequest is the PATCH decisions/{did} body: status must be
// accepted or rejected and the optional rationale reuses the proposal field.
type updateDecisionRequest struct {
	Status    string  `json:"status"`
	Rationale *string `json:"rationale"`
}
