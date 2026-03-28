// Package api assembles the HTTP server with all route groups.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/anuragxxd/conquest/internal/api/tes"
	"github.com/anuragxxd/conquest/internal/api/wes"
)

// NewRouter creates a chi router with all WES and TES routes registered,
// plus health, readiness, and startup probes matching the platform convention.
func NewRouter(wesHandler *wes.Handler, tesHandler *tes.Handler) http.Handler {
	r := chi.NewRouter()

	// Middleware.
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))

	// Kubernetes probes — matches infra chart pattern:
	//   startupProbe:  /startupz
	//   readinessProbe: /readyz
	//   livenessProbe:  /healthz
	okResponse := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
	r.Get("/healthz", okResponse)
	r.Get("/readyz", okResponse)
	r.Get("/startupz", okResponse)

	// WES routes.
	wesHandler.Routes(r)

	// TES routes (public).
	tesHandler.Routes(r)

	// TES routes (internal, run-scoped — used by Nextflow runner).
	tesHandler.InternalRoutes(r)

	return r
}
