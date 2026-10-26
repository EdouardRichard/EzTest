package server

import "net/http"

// handleHealthz is the liveness probe. It deliberately does not touch any
// dependency: it answers 200 as long as the process can serve requests.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
