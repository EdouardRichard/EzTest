package httpapi

import "net/http"

// Handlers groups the resource context's HTTP handlers so the composition root
// wires them into the server through one dependency. A nil handler is skipped by
// RegisterRoutes, which keeps a partially wired process (and most tests) from
// panicking at startup.
type Handlers struct {
	Catalog  *CatalogHandler
	Access   *AccessHandler
	Session  *SessionHandler
	CodeRepo *CodeRepoHandler
}

// RegisterRoutes mounts the resource endpoints on mux. Each non-nil handler
// registers its own routes and wraps every write and action route with
// adminGuard (the server passes the identity RequireAdmin guard; authentication
// is the global middleware), leaving the read routes authenticated-only.
func RegisterRoutes(mux *http.ServeMux, h Handlers, adminGuard func(http.Handler) http.Handler) {
	if h.Catalog != nil {
		h.Catalog.RegisterRoutes(mux, adminGuard)
	}
	if h.Access != nil {
		h.Access.RegisterRoutes(mux, adminGuard)
	}
	if h.Session != nil {
		h.Session.RegisterRoutes(mux, adminGuard)
	}
	if h.CodeRepo != nil {
		h.CodeRepo.RegisterRoutes(mux, adminGuard)
	}
}
