package httpapi

import (
	"context"
	"errors"
	"net/http"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// maxVersionBodyBytes caps the ordinary version request bodies. Metadata is
// bounded far below this (spec §7.2); the cap only protects the decoder.
const maxVersionBodyBytes int64 = 64 << 10

// VersionHandler serves the /api/versions endpoints (spec §7.2, §6.1).
type VersionHandler struct {
	versions  VersionService
	counts    VersionCountsReader
	resources versionapp.ResourceCatalog
}

// versionChangesService is the optional application slice behind
// GET /versions/{id}/changes (spec §7.2). *version/application.VersionService
// implements it; the handler type-asserts h.versions so the frozen VersionService
// consumer interface does not grow and a fake without the feed keeps compiling.
type versionChangesService interface {
	Changes(ctx context.Context, versionID int64, page domain.Page) (versionapp.VersionChangePage, error)
}

// RegisterRoutes mounts the version routes. Authentication is the server's
// global middleware: there is no per-route guard.
func (h *VersionHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/versions", h.List)
	mux.HandleFunc("POST /api/versions", h.Create)
	mux.HandleFunc("GET /api/versions/{id}", h.Get)
	mux.HandleFunc("PATCH /api/versions/{id}", h.Update)
	mux.HandleFunc("PUT /api/versions/{id}/bindings", h.UpdateBindings)
	mux.HandleFunc("POST /api/versions/{id}/start-clarification", h.StartClarification)
	mux.HandleFunc("POST /api/versions/{id}/enter-design", h.EnterDesign)
	mux.HandleFunc("GET /api/versions/{id}/changes", h.Changes)
}

// Create validates and inserts a draft version (spec §7.2) and returns 201.
func (h *VersionHandler) Create(w http.ResponseWriter, r *http.Request) {
	var request createVersionRequest
	if err := DecodeJSON(w, r, &request, maxVersionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	version, err := h.versions.Create(r.Context(), versionapp.CreateVersionCommand{
		Name:               request.Name,
		Label:              request.Label,
		Description:        request.Description,
		SystemID:           request.SystemID,
		EnvironmentID:      request.EnvironmentID,
		CodeRepositoryID:   request.CodeRepositoryID,
		CodeBranch:         request.CodeBranch,
		CodeBaselineCommit: request.CodeBaselineCommit,
	}, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeVersion(w, r, version, http.StatusCreated)
}

// List returns one cursor page ordered by updated_at DESC, id DESC.
func (h *VersionHandler) List(w http.ResponseWriter, r *http.Request) {
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	versions, hasMore, err := h.versions.List(r.Context(), versionapp.VersionFilter{
		Status: r.URL.Query().Get("status"),
		Page:   page,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	items := make([]versionDTO, 0, len(versions))
	for i := range versions {
		item, err := h.buildVersion(r.Context(), &versions[i])
		if err != nil {
			WriteServiceError(w, r, err)
			return
		}
		items = append(items, item)
	}
	next := ""
	if len(versions) > 0 {
		next = NextCursor(len(versions), versions[len(versions)-1].ID, hasMore)
	}
	WritePage(w, items, next, hasMore)
}

// Get returns one version with its counters and resolved bindings.
func (h *VersionHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	version, err := h.versions.Get(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeVersion(w, r, version, http.StatusOK)
}

// Update rewrites name/label/description in any status under the revision lock.
func (h *VersionHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request updateVersionRequest
	if err := DecodeJSON(w, r, &request, maxVersionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	// The PATCH body is partial; fill the omitted fields from the stored row so
	// UpdateMetadata's revision CAS still sees the authoritative values.
	current, err := h.versions.Get(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	name, label, description := current.Name, current.Label, current.Description
	if request.Name != nil {
		name = *request.Name
	}
	if request.Label != nil {
		label = *request.Label
	}
	if request.Description != nil {
		description = *request.Description
	}
	updated, err := h.versions.UpdateMetadata(r.Context(), id, request.Revision, name, label, description, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeVersion(w, r, updated, http.StatusOK)
}

// UpdateBindings replaces the whole binding set under the revision lock.
func (h *VersionHandler) UpdateBindings(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request putVersionBindingsRequest
	if err := DecodeJSON(w, r, &request, maxVersionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	updated, err := h.versions.UpdateBindings(r.Context(), id, request.Revision, versionapp.VersionBindings{
		SystemID:           request.SystemID,
		EnvironmentID:      request.EnvironmentID,
		CodeRepositoryID:   request.CodeRepositoryID,
		CodeBranch:         request.CodeBranch,
		CodeBaselineCommit: request.CodeBaselineCommit,
	}, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeVersion(w, r, updated, http.StatusOK)
}

// StartClarification moves a draft version to clarifying (spec §6.1).
func (h *VersionHandler) StartClarification(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	version, err := h.versions.StartClarification(r.Context(), id, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeVersion(w, r, version, http.StatusOK)
}

// EnterDesign moves a clarifying version to designing. The optional body
// acknowledges the soft gate; the success response carries the flattened
// warnings and is wrapped as {data:{version,warnings}} (lead ruling 2).
func (h *VersionHandler) EnterDesign(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request enterDesignRequest
	if err := DecodeOptionalJSON(w, r, &request, maxVersionBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	version, warnings, err := h.versions.EnterDesign(r.Context(), id, request.AcknowledgeUnclarified, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	dto, err := h.buildVersion(r.Context(), version)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, enterDesignResultDTO{
		Version:  dto,
		Warnings: warningMessages(warnings),
	})
}

// Changes returns the read-time derived change feed of one version (spec
// §7.2/D50): requirement creations, finished batches, published reports and
// created issues, newest first. It is an optional version-service slice: a
// process that did not wire it fails with an internal error instead of
// silently returning an empty feed.
func (h *VersionHandler) Changes(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	service, ok := h.versions.(versionChangesService)
	if !ok {
		WriteServiceError(w, r, errors.New("version: changes service is not configured"))
		return
	}
	result, err := service.Changes(r.Context(), id, page)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newVersionChangesDTO(result))
}

// writeVersion renders one version DTO with the given status.
func (h *VersionHandler) writeVersion(w http.ResponseWriter, r *http.Request, version *versionapp.Version, status int) {
	dto, err := h.buildVersion(r.Context(), version)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, status, dto)
}

// buildVersion resolves the counters and the P2 binding names of one version.
func (h *VersionHandler) buildVersion(ctx context.Context, version *versionapp.Version) (versionDTO, error) {
	counts := versionCountsView{}
	if h.counts != nil {
		resolved, err := h.counts.Counts(ctx, version.ID)
		if err != nil {
			return versionDTO{}, err
		}
		counts = versionCountsView{
			requirementCount:  resolved.RequirementCount,
			clarifiedCount:    resolved.ClarifiedCount,
			openQuestionCount: resolved.OpenQuestionCount,
		}
	}
	bindings, err := h.resolveBindings(ctx, version)
	if err != nil {
		return versionDTO{}, err
	}
	return newVersionDTO(version, counts, bindings), nil
}

// resolveBindings resolves the three optional P2 bindings for a version. A
// resource that no longer exists is rendered with available=false instead of
// failing the request (spec §6.8/§7.2); infrastructure failures still surface.
func (h *VersionHandler) resolveBindings(ctx context.Context, version *versionapp.Version) (versionBindingsDTO, error) {
	var view versionBindingsDTO
	if h.resources == nil {
		return view, nil
	}
	if version.SystemID != nil {
		ref, err := h.resources.GetSystem(ctx, *version.SystemID)
		if err != nil {
			resource, resolveErr := unavailableBinding(*version.SystemID, err)
			if resolveErr != nil {
				return view, resolveErr
			}
			view.System = resource
		} else {
			view.System = &versionResourceDTO{ID: ref.ID, Name: ref.Name, Available: ref.Available}
		}
	}
	if version.EnvironmentID != nil {
		ref, err := h.resources.GetEnvironment(ctx, *version.EnvironmentID)
		if err != nil {
			resource, resolveErr := unavailableBinding(*version.EnvironmentID, err)
			if resolveErr != nil {
				return view, resolveErr
			}
			view.Environment = resource
		} else {
			view.Environment = &versionResourceDTO{ID: ref.ID, Name: ref.Name, Available: ref.Available}
		}
	}
	if version.CodeRepositoryID != nil {
		ref, err := h.resources.GetCodeRepository(ctx, *version.CodeRepositoryID)
		if err != nil {
			resource, resolveErr := unavailableBinding(*version.CodeRepositoryID, err)
			if resolveErr != nil {
				return view, resolveErr
			}
			view.CodeRepository = resource
		} else {
			view.CodeRepository = &versionResourceDTO{ID: ref.ID, Name: ref.Name, Available: ref.Available}
		}
	}
	return view, nil
}

// unavailableBinding converts a missing P2 resource into an unavailable binding
// view and passes every other failure through (it becomes a 500).
func unavailableBinding(id int64, err error) (*versionResourceDTO, error) {
	var resourceErr *versionapp.ResourceError
	var domainErr *domain.Error
	if errors.As(err, &resourceErr) || (errors.As(err, &domainErr) && domainErr.Kind == domain.KindNotFound) {
		return &versionResourceDTO{ID: id, Available: false}, nil
	}
	return nil, err
}
