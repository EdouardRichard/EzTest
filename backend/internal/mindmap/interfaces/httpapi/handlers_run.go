package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// startRunRequest is the POST /runs body (spec §7.2 endpoint 17). headCommit is
// accepted for schema parity; P5 code-aware runs read the version's bound
// baseline commit, so the field is documented but not applied.
type startRunRequest struct {
	Kind           string   `json:"kind"`
	Mode           string   `json:"mode"`
	ConfirmReplace bool     `json:"confirmReplace"`
	NodeIDs        []string `json:"nodeIds"`
	SystemID       *int64   `json:"systemId"`
	EnvironmentID  *int64   `json:"environmentId"`
	SessionID      *int64   `json:"sessionId"`
	StartPaths     []string `json:"startPaths"`
	HeadCommit     string   `json:"headCommit"`
}

// startRun handles POST /runs (202). The exploration kind is administrator-only;
// every other kind is open to any authenticated user (spec §7.1/§7.2 endpoint 17).
func (h *Handler) startRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var body startRunRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	nodeIDs, err := parseIDStrings("nodeIds", body.NodeIDs)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	actor := actorID(ctx)

	switch body.Kind {
	case domain.RunKindDesign, domain.RunKindDataPlan:
		if h.deps.Runs == nil {
			writeServiceError(w, r, errors.New("mindmap: run coordinator is not configured"))
			return
		}
		run, err := h.deps.Runs.Start(ctx, versionID, actor, application.StartRunCommand{
			Kind:           body.Kind,
			Mode:           body.Mode,
			ConfirmReplace: body.ConfirmReplace,
			NodeIDs:        nodeIDs,
		})
		if err != nil {
			h.writeStartRunError(w, r, run, err)
			return
		}
		writeData(w, http.StatusAccepted, startRunResultDTO{RunID: idString(run.ID)})
	case domain.RunKindReview:
		if h.deps.Reviews == nil {
			writeServiceError(w, r, errors.New("mindmap: review service is not configured"))
			return
		}
		run, err := h.deps.Reviews.StartReview(ctx, versionID, actor)
		if err != nil {
			h.writeStartRunError(w, r, run, err)
			return
		}
		writeData(w, http.StatusAccepted, startRunResultDTO{RunID: idString(run.ID)})
	case domain.RunKindExplore:
		if err := requireAdmin(ctx, "forbidden", "an exploration run requires the admin role"); err != nil {
			writeServiceError(w, r, err)
			return
		}
		if h.deps.Explore == nil {
			writeServiceError(w, r, errors.New("mindmap: exploration service is not configured"))
			return
		}
		run, err := h.deps.Explore.Start(ctx, versionID, actor, application.ExploreCommand{
			SystemID:      body.SystemID,
			EnvironmentID: body.EnvironmentID,
			SessionID:     body.SessionID,
			StartPaths:    body.StartPaths,
		})
		if err != nil {
			h.writeStartRunError(w, r, run, err)
			return
		}
		h.launchExploration(run)
		writeData(w, http.StatusAccepted, startRunResultDTO{RunID: idString(run.ID)})
	default:
		writeServiceError(w, r, domain.NewInvalid("kind", "invalid_run_kind",
			"kind must be design, review, data_plan or explore"))
	}
}

// writeStartRunError maps a start failure. A run that already exists and was
// marked failed by the agent-runtime start is reported as 409 ai_unavailable with
// its runId in details, so the caller can read the failed run (spec §8.5 note of
// endpoint 17); any other failure uses the shared mapping.
func (h *Handler) writeStartRunError(w http.ResponseWriter, r *http.Request, run domain.CaseDesignRun, err error) {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) && domainErr != nil && domainErr.Reason == "ai_unavailable" && run.ID > 0 {
		writeDomainError(w, r, domainErr, map[string]any{"runId": idString(run.ID)})
		return
	}
	writeServiceError(w, r, err)
}

// launchExploration executes one queued exploration run asynchronously. The run
// outlives the HTTP request, so it gets a background context; the exploration
// service owns the queued→running→done/failed transitions and the
// case.run_finished event, and the handler writes no business table itself
// (spec §4.4/§8.5). The outcome is only logged.
func (h *Handler) launchExploration(run domain.CaseDesignRun) {
	go func() {
		result, err := h.deps.Explore.Run(context.Background(), run)
		if err != nil {
			h.logger.WarnContext(context.Background(), "mindmap: exploration run failed",
				"run_id", run.ID, "version_id", run.VersionID, "error", err)
			return
		}
		h.logger.InfoContext(context.Background(), "mindmap: exploration run finished",
			"run_id", run.ID, "version_id", run.VersionID, "status", result.Status,
			"discovered", result.Discovered, "imported", result.Imported)
	}()
}

// listRuns handles GET /runs (200): one cursor page of run summaries.
func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	limit, cursor, err := queryPage(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	runs, next, hasMore, err := h.deps.RunStore.List(r.Context(), versionID, kind, status, limit, cursor)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	rows := make([]caseRunDTO, 0, len(runs))
	for _, run := range runs {
		rows = append(rows, newRunDTO(run))
	}
	writePage(w, rows, next, hasMore)
}

// getRun handles GET /runs/{runId} (200).
func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	runID, err := pathID(r, "runId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	run, err := h.deps.RunStore.Get(r.Context(), versionID, runID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newRunDTO(run))
}
