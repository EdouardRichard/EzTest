package httpapi

import "net/http"

// Register mounts the 21 P3 path items (22 operations) defined by spec §9.2 on
// the shared mux. Every execution operation is available to any logged-in user;
// executor administration additionally requires the admin guard.
func (h *Handler) Register(mux *http.ServeMux, authenticated, admin func(http.Handler) http.Handler) {
	// Overview / intents.
	mux.Handle("GET /api/versions/{id}/execution/overview", authenticated(http.HandlerFunc(h.Overview)))
	mux.Handle("GET /api/versions/{id}/execution/intents", authenticated(http.HandlerFunc(h.ListIntents)))
	mux.Handle("POST /api/versions/{id}/execution/intents", authenticated(http.HandlerFunc(h.CreateIntent)))
	mux.Handle("POST /api/versions/{id}/execution/intents/{intentId}/cancel", authenticated(http.HandlerFunc(h.CancelIntent)))

	// Waves.
	mux.Handle("GET /api/versions/{id}/waves", authenticated(http.HandlerFunc(h.ListWaves)))
	mux.Handle("GET /api/versions/{id}/waves/{waveId}", authenticated(http.HandlerFunc(h.GetWave)))
	mux.Handle("POST /api/versions/{id}/waves/{waveId}/pause", authenticated(http.HandlerFunc(h.PauseWave)))
	mux.Handle("POST /api/versions/{id}/waves/{waveId}/resume", authenticated(http.HandlerFunc(h.ResumeWave)))
	mux.Handle("POST /api/versions/{id}/waves/{waveId}/cancel", authenticated(http.HandlerFunc(h.CancelWave)))
	mux.Handle("GET /api/versions/{id}/waves/{waveId}/case-runs", authenticated(http.HandlerFunc(h.ListWaveCaseRuns)))

	// Case runs.
	mux.Handle("GET /api/versions/{id}/case-runs/{caseRunId}", authenticated(http.HandlerFunc(h.GetCaseRun)))
	mux.Handle("POST /api/versions/{id}/case-runs/{caseRunId}/pause", authenticated(http.HandlerFunc(h.PauseCaseRun)))
	mux.Handle("POST /api/versions/{id}/case-runs/{caseRunId}/resume", authenticated(http.HandlerFunc(h.ResumeCaseRun)))

	// Evidence (read-only; no update/delete endpoints by design).
	mux.Handle("GET /api/versions/{id}/evidence", authenticated(http.HandlerFunc(h.ListEvidence)))
	mux.Handle("GET /api/versions/{id}/evidence/{evidenceId}", authenticated(http.HandlerFunc(h.GetEvidence)))
	mux.Handle("GET /api/versions/{id}/evidence/{evidenceId}/content", authenticated(http.HandlerFunc(h.DownloadEvidence)))

	// Human takeover requests.
	mux.Handle("GET /api/versions/{id}/human-requests", authenticated(http.HandlerFunc(h.ListHumanRequests)))
	mux.Handle("POST /api/human-requests/{humanRequestId}/resolve", authenticated(http.HandlerFunc(h.ResolveHumanRequest)))
	mux.Handle("POST /api/human-requests/{humanRequestId}/cancel", authenticated(http.HandlerFunc(h.CancelHumanRequest)))

	// Admin executor registry.
	mux.Handle("GET /api/admin/executors", admin(http.HandlerFunc(h.ListExecutors)))
	mux.Handle("POST /api/admin/executors/{executorId}/drain", admin(http.HandlerFunc(h.DrainExecutor)))
	mux.Handle("POST /api/admin/executors/{executorId}/activate", admin(http.HandlerFunc(h.ActivateExecutor)))
}
