package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// maxKnowledgeBodyBytes caps a knowledge request body: the 16 KiB body plus the
// bounded tags/source refs and the JSON framing.
const maxKnowledgeBodyBytes int64 = 32 << 10

// KnowledgeHandler serves the version knowledge endpoints (design spec
// §9.4/§11.1):
//
//	GET  /api/versions/{id}/knowledge
//	POST /api/versions/{id}/knowledge
//	PATCH /api/versions/{id}/knowledge/{kid}
//	POST /api/versions/{id}/knowledge/{kid}/review   (admin)
//	GET  /api/knowledge                              (approved; admin queues)
type KnowledgeHandler struct {
	knowledge KnowledgeService
}

// RegisterRoutes mounts the knowledge routes. Authentication is the global
// middleware; only the review route adds the per-route admin guard. The global
// read lets administrators request the draft/rejected queue, enforced in the
// handler because it shares one route with the approved-only read.
func (h *KnowledgeHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/versions/{id}/knowledge", h.List)
	mux.HandleFunc("POST /api/versions/{id}/knowledge", h.Create)
	mux.HandleFunc("PATCH /api/versions/{id}/knowledge/{kid}", h.Update)
	mux.Handle("POST /api/versions/{id}/knowledge/{kid}/review",
		identityhttp.RequireAdmin(http.HandlerFunc(h.Review)))
	mux.HandleFunc("GET /api/knowledge", h.ListGlobal)
}

// List returns one cursor page of a version's knowledge entries.
func (h *KnowledgeHandler) List(w http.ResponseWriter, r *http.Request) {
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
	entries, hasMore, err := h.knowledge.List(r.Context(), versionID,
		r.URL.Query().Get("status"), r.URL.Query().Get("kind"), page)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	next := ""
	if len(entries) > 0 {
		next = NextCursor(len(entries), entries[len(entries)-1].ID, hasMore)
	}
	WritePage(w, newKnowledgeDTOs(entries), next, hasMore)
}

// Create records one draft (201).
func (h *KnowledgeHandler) Create(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request createKnowledgeRequest
	if err := DecodeJSON(w, r, &request, maxKnowledgeBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	sourceRefs, err := request.sourceRefs()
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	entry, err := h.knowledge.Create(r.Context(), versionID, versionapp.CreateKnowledgeInput{
		Kind:       request.Kind,
		Title:      request.Title,
		Body:       request.Body,
		Tags:       request.Tags,
		SourceRefs: sourceRefs,
		Confidence: request.Confidence,
	}, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newKnowledgeDTO(entry))
}

// Update rewrites a draft; the service enforces creator/admin and the revision
// CAS.
func (h *KnowledgeHandler) Update(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	knowledgeID, err := PathID(r, "kid")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request updateKnowledgeRequest
	if err := DecodeJSON(w, r, &request, maxKnowledgeBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if request.Revision <= 0 {
		WriteServiceError(w, r, domain.NewInvalid("revision", "invalid_revision", "revision is required"))
		return
	}
	input := versionapp.UpdateKnowledgeInput{
		Revision:   request.Revision,
		Kind:       request.Kind,
		Title:      request.Title,
		Body:       request.Body,
		Tags:       request.Tags,
		Confidence: request.Confidence,
	}
	if request.SourceRefs != nil {
		sourceRefs, err := request.sourceRefs()
		if err != nil {
			WriteServiceError(w, r, err)
			return
		}
		input.SourceRefs = &sourceRefs
	}
	entry, err := h.knowledge.Update(r.Context(), versionID, knowledgeID, input,
		ActorID(r.Context()), knowledgeActorIsAdmin(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newKnowledgeDTO(entry))
}

// Review approves or rejects one draft. The route is admin-guarded; the request
// status is the target state (approved/rejected) and a rejection requires a
// non-empty reason.
func (h *KnowledgeHandler) Review(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	knowledgeID, err := PathID(r, "kid")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request reviewKnowledgeRequest
	if err := DecodeJSON(w, r, &request, maxKnowledgeBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if request.Revision <= 0 {
		WriteServiceError(w, r, domain.NewInvalid("revision", "invalid_revision", "revision is required"))
		return
	}
	var approve bool
	switch request.Status {
	case domain.KnowledgeApproved:
		approve = true
	case domain.KnowledgeRejected:
		approve = false
	default:
		WriteServiceError(w, r, domain.NewInvalid("status", "invalid_status",
			"status must be approved or rejected"))
		return
	}
	entry, err := h.knowledge.Review(r.Context(), versionID, knowledgeID,
		versionapp.ReviewKnowledgeInput{Revision: request.Revision, Approve: approve, Reason: request.Reason},
		ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newKnowledgeDTO(entry))
}

// ListGlobal serves the cross-version read. The default status is approved, so
// every logged-in user gets the read-only approved list; a non-admin asking for
// the draft/rejected queue is a 403.
func (h *KnowledgeHandler) ListGlobal(w http.ResponseWriter, r *http.Request) {
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = domain.KnowledgeApproved
	}
	if status != domain.KnowledgeApproved && !knowledgeActorIsAdmin(r.Context()) {
		platform.WriteError(w, r, http.StatusForbidden, platform.CodeForbidden,
			"admin role required")
		return
	}
	versionID, err := parseKnowledgeVersionID(r.URL.Query().Get("versionId"))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	entries, hasMore, err := h.knowledge.ListGlobal(r.Context(), versionID,
		status, r.URL.Query().Get("kind"), page)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	next := ""
	if len(entries) > 0 {
		next = NextCursor(len(entries), entries[len(entries)-1].ID, hasMore)
	}
	WritePage(w, newKnowledgeDTOs(entries), next, hasMore)
}

// parseKnowledgeVersionID parses the optional versionId query filter. The value
// is a P3 decimal string and must never be re-parsed through a float.
func parseKnowledgeVersionID(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, newBadRequest("versionId", "invalid_version_id",
			"versionId must be a positive decimal id")
	}
	return &id, nil
}

// knowledgeActorIsAdmin reports whether the authenticated caller is an
// administrator. The global auth middleware always stores the user first.
func knowledgeActorIsAdmin(ctx context.Context) bool {
	user, ok := identityhttp.UserFromContext(ctx)
	return ok && user.Role == identity.RoleAdmin
}

// createKnowledgeRequest is the POST body. source refs carry decimal string ids
// (D29).
type createKnowledgeRequest struct {
	Kind       string                      `json:"kind"`
	Title      string                      `json:"title"`
	Body       string                      `json:"body"`
	Confidence string                      `json:"confidence"`
	Tags       []string                    `json:"tags"`
	SourceRefs []knowledgeSourceRefRequest `json:"sourceRefs"`
}

// updateKnowledgeRequest is the partial PATCH body plus the mandatory expected
// revision.
type updateKnowledgeRequest struct {
	Revision   int                          `json:"revision"`
	Kind       *string                      `json:"kind"`
	Title      *string                      `json:"title"`
	Body       *string                      `json:"body"`
	Confidence *string                      `json:"confidence"`
	Tags       *[]string                    `json:"tags"`
	SourceRefs *[]knowledgeSourceRefRequest `json:"sourceRefs"`
}

// reviewKnowledgeRequest is the admin review body.
type reviewKnowledgeRequest struct {
	Revision int    `json:"revision"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

// knowledgeSourceRefRequest is one {kind,id} source reference with a decimal
// string id.
type knowledgeSourceRefRequest struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// sourceRefs maps the request refs to domain refs, rejecting malformed ids with
// the stable invalid_knowledge_source_refs reason.
func (r createKnowledgeRequest) sourceRefs() ([]domain.KnowledgeSourceRef, error) {
	return newKnowledgeSourceRefs(r.SourceRefs)
}

// sourceRefs maps the request refs to domain refs, rejecting malformed ids.
func (r updateKnowledgeRequest) sourceRefs() ([]domain.KnowledgeSourceRef, error) {
	return newKnowledgeSourceRefs(*r.SourceRefs)
}

// newKnowledgeSourceRefs converts the wire refs to the domain projection.
func newKnowledgeSourceRefs(refs []knowledgeSourceRefRequest) ([]domain.KnowledgeSourceRef, error) {
	if len(refs) == 0 {
		return []domain.KnowledgeSourceRef{}, nil
	}
	converted := make([]domain.KnowledgeSourceRef, 0, len(refs))
	for _, ref := range refs {
		id, err := strconv.ParseInt(strings.TrimSpace(ref.ID), 10, 64)
		if err != nil || id <= 0 {
			return nil, domain.NewInvalid("sourceRefs", "invalid_knowledge_source_refs",
				"sourceRefs.id must be a positive decimal id")
		}
		converted = append(converted, domain.KnowledgeSourceRef{Kind: ref.Kind, ID: id})
	}
	return converted, nil
}

// knowledgeDTO is the KnowledgeEntryDTO of spec §11.1. P3-owned ids are decimal
// strings; user ids stay numbers.
type knowledgeDTO struct {
	ID          string                  `json:"id"`
	VersionID   string                  `json:"versionId"`
	VersionName string                  `json:"versionName,omitempty"`
	Kind        string                  `json:"kind"`
	Title       string                  `json:"title"`
	Body        string                  `json:"body"`
	Tags        []string                `json:"tags"`
	SourceRefs  []knowledgeSourceRefDTO `json:"sourceRefs"`
	Status      string                  `json:"status"`
	Confidence  string                  `json:"confidence"`
	CreatedBy   int64                   `json:"createdBy"`
	ReviewedBy  *int64                  `json:"reviewedBy"`
	ReviewNote  string                  `json:"reviewNote"`
	Revision    int                     `json:"revision"`
	CreatedAt   time.Time               `json:"createdAt"`
	UpdatedAt   time.Time               `json:"updatedAt"`
}

// knowledgeSourceRefDTO is one {kind,id} source reference.
type knowledgeSourceRefDTO struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// newKnowledgeDTO maps one entry to the API shape.
func newKnowledgeDTO(entry *versionapp.KnowledgeEntry) knowledgeDTO {
	return knowledgeDTO{
		ID:          strconv.FormatInt(entry.ID, 10),
		VersionID:   strconv.FormatInt(entry.VersionID, 10),
		VersionName: entry.VersionName,
		Kind:        entry.Kind,
		Title:       entry.Title,
		Body:        entry.Body,
		Tags:        append([]string{}, entry.Tags...),
		SourceRefs:  newKnowledgeSourceRefDTOs(entry.SourceRefs),
		Status:      entry.Status,
		Confidence:  entry.Confidence,
		CreatedBy:   entry.CreatedBy,
		ReviewedBy:  entry.ReviewedBy,
		ReviewNote:  entry.ReviewNote,
		Revision:    entry.Revision,
		CreatedAt:   entry.CreatedAt,
		UpdatedAt:   entry.UpdatedAt,
	}
}

// newKnowledgeDTOs maps a page of entries in one pass.
func newKnowledgeDTOs(entries []versionapp.KnowledgeEntry) []knowledgeDTO {
	items := make([]knowledgeDTO, 0, len(entries))
	for index := range entries {
		items = append(items, newKnowledgeDTO(&entries[index]))
	}
	return items
}

// newKnowledgeSourceRefDTOs maps source refs to their wire shape.
func newKnowledgeSourceRefDTOs(refs []domain.KnowledgeSourceRef) []knowledgeSourceRefDTO {
	converted := make([]knowledgeSourceRefDTO, 0, len(refs))
	for _, ref := range refs {
		converted = append(converted, knowledgeSourceRefDTO{
			Kind: ref.Kind,
			ID:   strconv.FormatInt(ref.ID, 10),
		})
	}
	return converted
}
