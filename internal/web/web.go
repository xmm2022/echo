// Package web serves Echo's minimal admin dashboard (spec §12: templ + htmx).
// v0.1 is read-only oversight: list jobs and open hash conflicts, plus a dismiss
// action. The dashboard shell at "/" is public and data-free; the HTML fragments
// it loads (/ui/*) and the JSON API are behind the auth middleware.
package web

import (
	"database/sql"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
	"github.com/xmm2022/echo/internal/web/templates"
)

//go:embed static
var staticFS embed.FS

const (
	dashboardJobLimit      = 50
	dashboardConflictLimit = 100
	// v0.2 management fragment limits mirror the JSON API defaults: small, bounded
	// reads of the same sqlc queries.
	dashboardManagementLimit = 100
	dashboardPlaybackLimit   = 200
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

// MountUI registers the authenticated, admin-only HTML fragment routes. Callers
// mount it inside the authed group alongside the API; this method adds the
// admin-scope check for the dashboard fragment tree.
func (d Deps) MountUI(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(requireAdmin)
		r.Get("/ui/jobs", d.UIJobs)
		r.Get("/ui/conflicts", d.UIConflicts)
		r.Get("/ui/emby/servers", d.UIEmbyServers)
		r.Get("/ui/emby/user-links", d.UIEmbyUserLinks)
		r.Get("/ui/emby/library-mappings", d.UIEmbyLibraryMappings)
		r.Get("/ui/account-pools", d.UIAccountPools)
		r.Get("/ui/quota/policies", d.UIQuotaPolicies)
		r.Get("/ui/quota/usage", d.UIQuotaUsage)
		r.Get("/ui/playback/sessions", d.UIPlaybackSessions)
		r.Get("/ui/playback/events", d.UIPlaybackEvents)
		r.Get("/ui/discovery/subscriptions", d.UIDiscoverySubscriptions)
		r.Get("/ui/discovery/sources", d.UIDiscoverySources)
		r.Get("/ui/discovery/producer-profiles", d.UIDiscoveryProducerProfiles)
		r.Get("/ui/discovery/rule-profiles", d.UIDiscoveryRuleProfiles)
		r.Get("/ui/discovery/candidates", d.UIDiscoveryCandidates)
		r.Get("/ui/discovery/matches", d.UIDiscoveryMatches)
		r.Get("/ui/discovery/runs", d.UIDiscoveryRuns)
	})
}

func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.FromContext(r.Context()).HasScope("admin") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireAdminFragment(w http.ResponseWriter, r *http.Request) bool {
	user := auth.FromContext(r.Context())
	if !user.HasScope("admin") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
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

// UIEmbyServers serves GET /ui/emby/servers — the Emby servers management fragment.
// It uses ListEmbyServers (whose row type omits api_key_ref) so no secret reference
// can reach the dashboard.
func (d Deps) UIEmbyServers(w http.ResponseWriter, r *http.Request) {
	rows, err := d.Store.ListEmbyServers(r.Context(), queries.ListEmbyServersParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list emby servers", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.EmbyServersTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render emby servers", "err", err)
	}
}

// UIEmbyUserLinks serves GET /ui/emby/user-links — the Emby user-link management fragment.
func (d Deps) UIEmbyUserLinks(w http.ResponseWriter, r *http.Request) {
	rows, err := d.Store.ListEmbyUserLinks(r.Context(), queries.ListEmbyUserLinksParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list emby user links", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.EmbyUserLinksTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render emby user links", "err", err)
	}
}

// UIEmbyLibraryMappings serves GET /ui/emby/library-mappings — the library-mapping
// management fragment.
func (d Deps) UIEmbyLibraryMappings(w http.ResponseWriter, r *http.Request) {
	rows, err := d.Store.ListEmbyLibraryMappings(r.Context(), queries.ListEmbyLibraryMappingsParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list emby library mappings", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.EmbyLibraryMappingsTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render emby library mappings", "err", err)
	}
}

// UIAccountPools serves GET /ui/account-pools — the account-pool-assignment management
// fragment. It lists every user's assignments (empty echo_user_id filter) because /ui is
// the admin management dashboard (see the MountUI security note on admin-gating).
func (d Deps) UIAccountPools(w http.ResponseWriter, r *http.Request) {
	rows, err := d.Store.ListAccountPoolAssignments(r.Context(), queries.ListAccountPoolAssignmentsParams{
		Column1:    "",
		EchoUserID: "",
		Limit:      dashboardManagementLimit,
		Offset:     0,
	})
	if err != nil {
		d.logger().Error("web: list account pool assignments", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.AccountPoolsTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render account pool assignments", "err", err)
	}
}

// UIQuotaPolicies serves GET /ui/quota/policies — the quota-policy management fragment.
func (d Deps) UIQuotaPolicies(w http.ResponseWriter, r *http.Request) {
	rows, err := d.Store.ListQuotaPolicies(r.Context(), queries.ListQuotaPoliciesParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list quota policies", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.QuotaPoliciesTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render quota policies", "err", err)
	}
}

// UIQuotaUsage serves GET /ui/quota/usage — a read-only usage overview. There is no
// all-users usage query, so it shows the cluster active-session count plus the
// quota-policy overview (the limits that bound usage). Secret-free by design.
func (d Deps) UIQuotaUsage(w http.ResponseWriter, r *http.Request) {
	active, err := d.Store.CountActivePlaybackSessions(r.Context())
	if err != nil {
		d.logger().Error("web: count active sessions", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	policies, err := d.Store.ListQuotaPolicies(r.Context(), queries.ListQuotaPoliciesParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list quota policies for usage", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.QuotaUsage(active, policies).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render quota usage", "err", err)
	}
}

// UIPlaybackSessions serves GET /ui/playback/sessions — a READ-ONLY fragment of recent
// playback sessions. ListPlaybackSessionsForAdmin returns rows that include the live-token
// selector and token_hash; the PlaybackSessionsTable template renders neither, matching
// the redaction in the JSON API.
func (d Deps) UIPlaybackSessions(w http.ResponseWriter, r *http.Request) {
	rows, err := d.Store.ListPlaybackSessionsForAdmin(r.Context(), queries.ListPlaybackSessionsForAdminParams{Limit: dashboardPlaybackLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list playback sessions", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.PlaybackSessionsTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render playback sessions", "err", err)
	}
}

// UIPlaybackEvents serves GET /ui/playback/events — a READ-ONLY fragment of recent
// playback events across all users (empty echo_user_id filter; /ui is the admin management
// dashboard — see the MountUI security note on admin-gating).
func (d Deps) UIPlaybackEvents(w http.ResponseWriter, r *http.Request) {
	rows, err := d.Store.ListPlaybackEventsForAdmin(r.Context(), queries.ListPlaybackEventsForAdminParams{
		Column1:    "",
		EchoUserID: sql.NullString{},
		Limit:      dashboardPlaybackLimit,
		Offset:     0,
	})
	if err != nil {
		d.logger().Error("web: list playback events", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.PlaybackEventsTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render playback events", "err", err)
	}
}

func (d Deps) UIDiscoverySubscriptions(w http.ResponseWriter, r *http.Request) {
	if !requireAdminFragment(w, r) {
		return
	}
	rows, err := d.Store.ListDiscoverySubscriptions(r.Context(), queries.ListDiscoverySubscriptionsParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list discovery subscriptions", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.DiscoverySubscriptionsTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render discovery subscriptions", "err", err)
	}
}

func (d Deps) UIDiscoverySources(w http.ResponseWriter, r *http.Request) {
	if !requireAdminFragment(w, r) {
		return
	}
	rows, err := d.Store.ListDiscoverySources(r.Context(), queries.ListDiscoverySourcesParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list discovery sources", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.DiscoverySourcesTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render discovery sources", "err", err)
	}
}

func (d Deps) UIDiscoveryProducerProfiles(w http.ResponseWriter, r *http.Request) {
	if !requireAdminFragment(w, r) {
		return
	}
	rows, err := d.Store.ListDiscoveryProducerProfiles(r.Context(), queries.ListDiscoveryProducerProfilesParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list discovery producer profiles", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.DiscoveryProducerProfilesTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render discovery producer profiles", "err", err)
	}
}

func (d Deps) UIDiscoveryRuleProfiles(w http.ResponseWriter, r *http.Request) {
	if !requireAdminFragment(w, r) {
		return
	}
	rows, err := d.Store.ListRuleProfiles(r.Context(), queries.ListRuleProfilesParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list discovery rule profiles", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.DiscoveryRuleProfilesTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render discovery rule profiles", "err", err)
	}
}

func (d Deps) UIDiscoveryCandidates(w http.ResponseWriter, r *http.Request) {
	if !requireAdminFragment(w, r) {
		return
	}
	rows, err := d.Store.ListDiscoveredResourcesRedacted(r.Context(), queries.ListDiscoveredResourcesRedactedParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list discovery candidates", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.DiscoveryCandidatesTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render discovery candidates", "err", err)
	}
}

func (d Deps) UIDiscoveryMatches(w http.ResponseWriter, r *http.Request) {
	if !requireAdminFragment(w, r) {
		return
	}
	rows, err := d.Store.ListSubscriptionMatchesForAdmin(r.Context(), queries.ListSubscriptionMatchesForAdminParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list discovery matches", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.DiscoveryMatchesTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render discovery matches", "err", err)
	}
}

func (d Deps) UIDiscoveryRuns(w http.ResponseWriter, r *http.Request) {
	if !requireAdminFragment(w, r) {
		return
	}
	rows, err := d.Store.ListDiscoveryRuns(r.Context(), queries.ListDiscoveryRunsParams{Limit: dashboardManagementLimit, Offset: 0})
	if err != nil {
		d.logger().Error("web: list discovery runs", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.DiscoveryRunsTable(rows).Render(r.Context(), w); err != nil {
		d.logger().Error("web: render discovery runs", "err", err)
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
