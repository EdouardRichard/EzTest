package httpapi

import (
	"net/http"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// maxDecisionBodyBytes caps the decision request bodies. The topic is bounded to
// 300 characters and decision/rationale to 5000 by the domain (spec §6.6).
const maxDecisionBodyBytes int64 = 32 << 10

// DecisionHandler serves the clarification decision endpoints (spec §7.6,
// §6.6).
type DecisionHandler struct {
	clarification ClarificationService
}

// RegisterRoutes mounts the decision routes. Authentication is the server's
// global middleware: there is no per-route guard.
func (h *DecisionHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/versions/{id}/decisions", h.List)
	mux.HandleFunc("POST /api/versions/{id}/decisions", h.Create)
	mux.HandleFunc("PATCH /api/versions/{id}/decisions/{did}", h.Update)
}

// List returns one cursor page of decisions ordered by id ascending, optionally
// filtered by status.
func (h *DecisionHandler) List(w http.ResponseWriter, r *http.Request) {
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
	decisions, hasMore, err := h.clarification.ListDecisions(r.Context(), versionID,
		r.URL.Query().Get("status"), page)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	next := ""
	if len(decisions) > 0 {
		next = NextCursor(len(decisions), decisions[len(decisions)-1].ID, hasMore)
	}
	WritePage(w, newDecisionDTOs(decisions), next, hasMore)
}

// Create records one manual decision. A human-sourced decision is accepted
// immediately (201) and accepting it answers a linked open question in the same
// service transaction.
func (h *DecisionHandler) Create(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request createDecisionRequest
	if err := DecodeJSON(w, r, &request, maxDecisionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	decision, err := h.clarification.CreateDecision(r.Context(), versionID, versionapp.DecisionInput{
		RequirementID: request.RequirementID,
		QuestionID:    request.QuestionID,
		Topic:         request.Topic,
		Decision:      request.Decision,
		Rationale:     request.Rationale,
	}, ActorID(r.Context()), domain.SourceUser)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newDecisionDTO(decision))
}

// Update accepts or rejects one proposed decision. The status must be accepted
// or rejected; the optional rationale reuses the proposal field (spec §7.6).
func (h *DecisionHandler) Update(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	decisionID, err := PathID(r, "did")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request updateDecisionRequest
	if err := DecodeJSON(w, r, &request, maxDecisionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var accept bool
	switch request.Status {
	case domain.DecisionAccepted:
		accept = true
	case domain.DecisionRejected:
		accept = false
	default:
		WriteServiceError(w, r, domain.NewInvalid("status", "invalid_status",
			"status must be accepted or rejected"))
		return
	}
	var rationale []string
	if request.Rationale != nil {
		rationale = []string{*request.Rationale}
	}
	decision, err := h.clarification.Decide(r.Context(), versionID, decisionID, accept,
		ActorID(r.Context()), rationale...)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newDecisionDTO(decision))
}
