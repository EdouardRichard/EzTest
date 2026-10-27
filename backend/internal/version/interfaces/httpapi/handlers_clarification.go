package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const (
	// maxClarificationBodyBytes caps the clarification request bodies. The
	// message itself is bounded to 5000 characters by the service.
	maxClarificationBodyBytes int64 = 64 << 10
	// reconRequirementPageSize is the requirement page size walked when the
	// context-recon parts are built from every requirement of the version.
	reconRequirementPageSize = 100
)

// aiUnavailableErrors are the P1 agent/LLM startup failures of spec §8.6: the
// run could not start because the AI runtime is not configured, unsupported or
// out of capacity. They map to the P3 503 unavailable/ai_unavailable
// (retryable=true) instead of a generic 500.
var aiUnavailableErrors = []error{
	agentdomain.ErrLLMNotConfigured,
	agentdomain.ErrToolsUnsupported,
	agentdomain.ErrCapacity,
}

// ClarificationHandler serves the session, message and one-shot run endpoints
// under /api/versions/{id}/clarification (spec §7.5, §6.5, §8.4).
type ClarificationHandler struct {
	clarification ClarificationService
	messages      ClarificationMessagesReader
	requirements  RequirementService
	attachments   AttachmentService
	aiReady       func(context.Context) error
}

// RegisterRoutes mounts the clarification routes. Authentication is the
// server's global middleware: there is no per-route guard.
func (h *ClarificationHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/versions/{id}/clarification", h.Overview)
	mux.HandleFunc("POST /api/versions/{id}/clarification/sessions", h.StartSession)
	mux.HandleFunc("POST /api/versions/{id}/clarification/sessions/{sid}/complete", h.CompleteSession)
	mux.HandleFunc("GET /api/versions/{id}/clarification/messages", h.ListMessages)
	mux.HandleFunc("POST /api/versions/{id}/clarification/messages", h.SendMessage)
	mux.HandleFunc("POST /api/versions/{id}/clarification/analyze", h.Analyze)
	mux.HandleFunc("POST /api/versions/{id}/clarification/recon", h.Recon)
}

// Overview returns the active session (null when none), the per-status counters
// and the agent-maintained context summary.
func (h *ClarificationHandler) Overview(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	session, err := h.clarification.CurrentSession(r.Context(), versionID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	counts, err := h.clarification.Counts(r.Context(), versionID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newClarificationOverviewDTO(session, counts))
}

// StartSession starts one clarification session (201) with the narrow started
// body of spec §7.5.
func (h *ClarificationHandler) StartSession(w http.ResponseWriter, r *http.Request) {
	if !h.ensureAIReady(w, r) {
		return
	}
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	session, err := h.clarification.StartSession(r.Context(), versionID, ActorID(r.Context()))
	if err != nil {
		writeClarificationError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newClarificationSessionStartedDTO(session))
}

// CompleteSession completes one active session and returns the full session DTO.
func (h *ClarificationHandler) CompleteSession(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	sessionID, err := PathID(r, "sid")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	session, err := h.clarification.CompleteSession(r.Context(), versionID, sessionID, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newClarificationSessionDTO(session))
}

// ListMessages returns the persisted P1 conversation projection of the version's
// latest session (active or completed, so history survives completion), oldest
// first and with role=system filtered out. before_id is the raw numeric P1
// message id (spec §7.5) and next_cursor is its decimal string form.
func (h *ClarificationHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	limit, err := ParseQueryLimit(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	beforeID, err := ParseBeforeID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	session, err := h.clarification.LatestSession(r.Context(), versionID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if session == nil || session.AgentSessionID == nil {
		// Reading history is not a state transition: a version whose latest
		// session is completed still exposes the persisted conversation, and a
		// version without any session has an empty history, not a 409.
		WritePage(w, []clarificationMessageDTO{}, "", false)
		return
	}
	if h.messages == nil {
		WriteServiceError(w, r, errors.New("version: clarification message reader is not configured"))
		return
	}
	messages, hasMore, err := h.messages.Messages(r.Context(), *session.AgentSessionID, beforeID, limit)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if len(messages) == 0 {
		hasMore = false
	}
	visible := make([]versionapp.AgentMessage, 0, len(messages))
	for i := range messages {
		if messages[i].Role == "system" {
			continue
		}
		visible = append(visible, messages[i])
	}
	next := ""
	if hasMore && len(messages) > 0 {
		// The P1 page is ordered ascending, so the oldest raw row (even a
		// filtered one) is the exclusive before_id of the next page.
		next = strconv.FormatInt(messages[0].ID, 10)
	}
	WritePage(w, newClarificationMessageDTOs(visible), next, hasMore)
}

// SendMessage starts one text run on the active session (202). An unavailable
// AI runtime is a retryable 503 ai_unavailable so the UI keeps the draft.
func (h *ClarificationHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	if !h.ensureAIReady(w, r) {
		return
	}
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request sendClarificationMessageRequest
	if err := DecodeJSON(w, r, &request, maxClarificationBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	result, err := h.clarification.SendMessage(r.Context(), versionID, request.Message, ActorID(r.Context()))
	if err != nil {
		writeClarificationError(w, r, err)
		return
	}
	WriteData(w, http.StatusAccepted, newSendClarificationMessageResultDTO(result))
}

// Analyze starts the one-shot requirement-analysis run (202). The run is
// text-only: the analysis instruction is owned by the service.
func (h *ClarificationHandler) Analyze(w http.ResponseWriter, r *http.Request) {
	if !h.ensureAIReady(w, r) {
		return
	}
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	result, err := h.clarification.StartAnalysis(r.Context(), versionID, ActorID(r.Context()))
	if err != nil {
		writeClarificationError(w, r, err)
		return
	}
	WriteData(w, http.StatusAccepted, newClarificationRunResultDTO(result))
}

// Recon starts the one-shot context-recon run (202) with the version attachment
// parts: bounded text summaries and at most two image references. The
// instruction text part is prepended by StartReconWithParts.
func (h *ClarificationHandler) Recon(w http.ResponseWriter, r *http.Request) {
	if !h.ensureAIReady(w, r) {
		return
	}
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	parts, err := h.reconParts(r.Context(), versionID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	result, err := h.clarification.StartReconWithParts(r.Context(), versionID, parts, ActorID(r.Context()))
	if err != nil {
		writeClarificationError(w, r, err)
		return
	}
	WriteData(w, http.StatusAccepted, newClarificationRunResultDTO(result))
}

// reconParts builds the attachment parts of one recon run from every requirement
// of the version: text reads are capped at 64 KiB per file and 200 KiB in total
// for the run, and at most two images at most 2 MiB each are injected as
// attachment:<id> refs (spec §6.7/§8.4). An oversized image is not injected;
// instead a small deterministic text note announces the skipped file and is
// charged to the same 200 KiB text budget. A version without attachments yields
// nil parts.
func (h *ClarificationHandler) reconParts(ctx context.Context, versionID int64) ([]versionapp.AgentPart, error) {
	if h.requirements == nil || h.attachments == nil {
		return nil, nil
	}
	var parts []versionapp.AgentPart
	textBudget := domain.MaxInjectedTextBytesPerRun
	images := 0
	cursor := ""
	for {
		requirements, hasMore, err := h.requirements.List(ctx, versionID, "",
			domain.Page{Limit: reconRequirementPageSize, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for i := range requirements {
			attachments, err := h.attachments.List(ctx, versionID, requirements[i].ID)
			if err != nil {
				return nil, err
			}
			for j := range attachments {
				attachment := &attachments[j]
				switch attachment.Kind {
				case domain.AttachmentImage:
					if attachment.SizeBytes > domain.MaxAgentImageBytes {
						// The image cannot be injected, but a small deterministic
						// note lets the agent tell the user the file was skipped.
						// It is charged to the text budget like any injected text;
						// past the budget the note is omitted (spec §6.7).
						note := reconOversizedImageNote(attachment.Filename, attachment.SizeBytes)
						if len(note) <= textBudget {
							textBudget -= len(note)
							parts = append(parts, versionapp.AgentPart{Type: "text", Text: note})
						}
						continue
					}
					if images >= domain.MaxAgentImagesPerRun {
						continue
					}
					parts = append(parts, versionapp.AgentPart{
						Type:     "image",
						ImageRef: "attachment:" + strconv.FormatInt(attachment.ID, 10),
					})
					images++
				case domain.AttachmentText:
					if textBudget <= 0 {
						continue
					}
					maxBytes := textBudget
					if maxBytes > domain.MaxInjectedTextBytesPerFile {
						maxBytes = domain.MaxInjectedTextBytesPerFile
					}
					content, err := h.attachments.ReadText(ctx, versionID, attachment.ID, maxBytes)
					if err != nil {
						return nil, err
					}
					if content == "" {
						continue
					}
					// The service already clamps to maxBytes; clamp again so one
					// broken adapter cannot push the run over its text budget.
					if len(content) > maxBytes {
						content = strings.ToValidUTF8(content[:maxBytes], "")
						if content == "" {
							continue
						}
					}
					textBudget -= len(content)
					if textBudget < 0 {
						textBudget = 0
					}
					parts = append(parts, versionapp.AgentPart{
						Type: "text",
						Text: reconAttachmentText(attachment.Filename, content),
					})
				}
			}
		}
		if !hasMore || len(requirements) == 0 {
			break
		}
		cursor = platform.EncodeCursor(requirements[len(requirements)-1].ID)
	}
	return parts, nil
}

// reconAttachmentText frames one attachment as untrusted analysis material; the
// run instruction tells the agent to treat it as data, never as commands.
func reconAttachmentText(filename, content string) string {
	return fmt.Sprintf("Attachment %s:\n%s", filename, content)
}

// reconOversizedImageNote renders the deterministic text part that stands in for
// an image too large to inject (spec §6.7), so the agent can mention the skipped
// file to the user instead of losing it silently.
func reconOversizedImageNote(filename string, sizeBytes int64) string {
	return fmt.Sprintf("Attachment %s was too large to inject (%d bytes, limit %d bytes) and was not included.",
		filename, sizeBytes, domain.MaxAgentImageBytes)
}

// ensureAIReady runs the optional AI readiness probe before an endpoint starts
// an agent run. A nil probe assumes availability (unit tests and partially wired
// processes); a probe error is the retryable 503 ai_unavailable of spec §8.6.
func (h *ClarificationHandler) ensureAIReady(w http.ResponseWriter, r *http.Request) bool {
	if h.aiReady == nil {
		return true
	}
	if err := h.aiReady(r.Context()); err != nil {
		writeAIUnavailable(w, r)
		return false
	}
	return true
}

// writeClarificationError maps the P1 agent/LLM startup failures to the P3 503
// unavailable reason ai_unavailable (retryable) and every other failure to the
// shared error mapping.
func writeClarificationError(w http.ResponseWriter, r *http.Request, err error) {
	if isAIUnavailable(err) {
		writeAIUnavailable(w, r)
		return
	}
	WriteServiceError(w, r, err)
}

// writeAIUnavailable renders the spec §8.6 503 unavailable/ai_unavailable body
// shared by the readiness probe and the P1 startup failures.
func writeAIUnavailable(w http.ResponseWriter, r *http.Request) {
	message := "the AI runtime is unavailable"
	platform.WriteError(w, r, http.StatusServiceUnavailable, platform.CodeUnavailable, message,
		platform.WithRetryable(true),
		platform.WithDetails(map[string]any{
			"reason":  "ai_unavailable",
			"message": message,
		}))
}

// isAIUnavailable reports whether err is one of the P1 agent startup failures.
func isAIUnavailable(err error) bool {
	for _, target := range aiUnavailableErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
