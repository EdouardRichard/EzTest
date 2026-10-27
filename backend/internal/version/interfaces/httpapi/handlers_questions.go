package httpapi

import (
	"net/http"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// maxQuestionBodyBytes caps the question request bodies. The question itself is
// bounded to 2000 characters and the answer to 5000 by the domain (spec §6.6).
const maxQuestionBodyBytes int64 = 32 << 10

// QuestionHandler serves the clarification question endpoints (spec §7.6,
// §6.3/§6.6).
type QuestionHandler struct {
	clarification ClarificationService
}

// RegisterRoutes mounts the question routes. Authentication is the server's
// global middleware: there is no per-route guard.
func (h *QuestionHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/versions/{id}/clarification/questions", h.List)
	mux.HandleFunc("POST /api/versions/{id}/clarification/questions", h.Create)
	mux.HandleFunc("PATCH /api/versions/{id}/clarification/questions/{qid}", h.Update)
}

// List returns one cursor page of questions ordered by id ascending, optionally
// filtered by status and requirementId (a P3 decimal string).
func (h *QuestionHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	requirementID, err := ParseOptionalQueryID(r, "requirementId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	questions, hasMore, err := h.clarification.ListQuestions(r.Context(), versionID,
		r.URL.Query().Get("status"), requirementID, page)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	next := ""
	if len(questions) > 0 {
		next = NextCursor(len(questions), questions[len(questions)-1].ID, hasMore)
	}
	WritePage(w, newQuestionDTOs(questions), next, hasMore)
}

// Create adds one open, user-sourced question (201). requirementId is optional
// and must belong to the version.
func (h *QuestionHandler) Create(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request createQuestionRequest
	if err := DecodeJSON(w, r, &request, maxQuestionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	question, err := h.clarification.UpsertQuestion(r.Context(), versionID, versionapp.QuestionInput{
		RequirementID: request.RequirementID,
		Question:      request.Question,
	}, ActorID(r.Context()), domain.SourceUser)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newQuestionDTO(question))
}

// Update applies one question PATCH: a status transition (answer, dismiss,
// reopen) and/or a text correction. Answering requires an answer; only the
// documented transitions of spec §6.3/§7.6 are accepted.
func (h *QuestionHandler) Update(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	questionID, err := PathID(r, "qid")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request updateQuestionRequest
	if err := DecodeJSON(w, r, &request, maxQuestionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if request.Status == nil && request.Question == nil && request.Answer == nil {
		WriteServiceError(w, r, domain.NewInvalid("", "invalid_request",
			"at least one of status, question or answer is required"))
		return
	}

	// A text-only PATCH updates the text and returns the question unchanged in
	// state. A PATCH that also carries a transition applies the transition first
	// so an illegal transition never leaves a partial text write behind; a
	// combined text + transition is still two writes (not one transaction).
	if request.Status == nil {
		if request.Question == nil {
			WriteServiceError(w, r, domain.NewInvalid("status", "invalid_status",
				"status is required to answer, dismiss or reopen a question"))
			return
		}
		updated, err := h.clarification.UpsertQuestion(r.Context(), versionID, versionapp.QuestionInput{
			QuestionID: &questionID,
			Question:   *request.Question,
		}, ActorID(r.Context()), domain.SourceUser)
		if err != nil {
			WriteServiceError(w, r, err)
			return
		}
		WriteData(w, http.StatusOK, newQuestionDTO(updated))
		return
	}

	var question *versionapp.Question
	switch *request.Status {
	case domain.QuestionAnswered:
		answer := ""
		if request.Answer != nil {
			answer = *request.Answer
		}
		question, err = h.clarification.AnswerQuestion(r.Context(), versionID, questionID,
			answer, ActorID(r.Context()), domain.AnsweredViaUI)
	case domain.QuestionDismissed:
		question, err = h.clarification.DismissQuestion(r.Context(), versionID, questionID,
			ActorID(r.Context()))
	case domain.QuestionOpen:
		question, err = h.clarification.ReopenQuestion(r.Context(), versionID, questionID,
			ActorID(r.Context()))
	default:
		WriteServiceError(w, r, domain.NewInvalid("status", "invalid_status",
			"status must be answered, dismissed or open"))
		return
	}
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if request.Question != nil {
		updated, err := h.clarification.UpsertQuestion(r.Context(), versionID, versionapp.QuestionInput{
			QuestionID: &questionID,
			Question:   *request.Question,
		}, ActorID(r.Context()), domain.SourceUser)
		if err != nil {
			WriteServiceError(w, r, err)
			return
		}
		question = updated
	}
	WriteData(w, http.StatusOK, newQuestionDTO(question))
}
