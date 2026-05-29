// Package web serves Echo's minimal admin dashboard (spec §12: templ + htmx).
// v0.1 is read-only oversight: list jobs and open hash conflicts, plus a dismiss
// action. The dashboard shell at "/" is public and data-free; the HTML fragments
// it loads (/ui/*) and the JSON API are behind the auth middleware.
package web

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
	"github.com/xmm2022/echo/internal/web/templates"
)

//go:embed static
var staticFS embed.FS

const (
	dashboardJobLimit      = 50
	dashboardConflictLimit = 100
)

// Deps wires the dashboard handlers to the store.
type Deps struct {
	Store  *store.Store
	Logger *slog.Logger
}

func (d Deps) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// Index serves GET / — the public dashboard shell.
func (d Deps) Index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.Index().Render(r.Context(), w); err != nil {
		d.logger().Error("web: render index", "err", err)
	}
}

// MountUI registers the authenticated HTML fragment routes. Callers mount it
// inside the authed group alongside the API.
func (d Deps) MountUI(r chi.Router) {
	r.Get("/ui/jobs", d.UIJobs)
	r.Get("/ui/conflicts", d.UIConflicts)
}

// UIJobs serves GET /ui/jobs — the recent-jobs HTML fragment.
func (d Deps) UIJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := d.Store.ListJobs(r.Context(), queries.ListJobsParams{Limit: dashboardJobLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list jobs", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.JobsTable(jobs).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render jobs", "err", err)
	}
}

// UIConflicts serves GET /ui/conflicts — the open-conflicts HTML fragment.
func (d Deps) UIConflicts(w http.ResponseWriter, r *http.Request) {
	conflicts, err := d.Store.ListOpenHashConflicts(r.Context(), queries.ListOpenHashConflictsParams{Limit: dashboardConflictLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list conflicts", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ConflictsTable(conflicts).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render conflicts", "err", err)
	}
}

// Static returns the handler that serves the vendored assets under /static/
// (htmx.min.js, app.js). The embed path is a compile-time constant, so fs.Sub
// never fails in practice.
func Static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("web: embed static subtree: " + err.Error())
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}
