package handlers

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/embyproxy"
	"github.com/xmm2022/echo/internal/store/queries"
)

// MountV02Management registers the v0.2 management JSON API: Emby server / user-link
// / library-mapping CRUD and account-pool / quota-policy administration (all
// admin-only), plus the owner-scoped playback and quota read endpoints. Callers
// mount it inside the same authenticated group as Mount so every route is behind
// auth; the per-handler requireAdmin / requestedUserOrSelf checks layer the
// admin-vs-owner authorization on top.
func (d APIDeps) MountV02Management(r chi.Router) {
	r.Get("/api/emby/servers", d.ListEmbyServers)
	r.Post("/api/emby/servers", d.UpsertEmbyServer)
	r.Patch("/api/emby/servers/{id}", d.UpsertEmbyServer)
	r.Get("/api/emby/user-links", d.ListEmbyUserLinks)
	r.Post("/api/emby/user-links", d.UpsertEmbyUserLink)
	r.Patch("/api/emby/user-links/{id}", d.UpsertEmbyUserLink)
	r.Get("/api/emby/library-mappings", d.ListEmbyLibraryMappings)
	r.Post("/api/emby/library-mappings", d.CreateEmbyLibraryMapping)
	r.Patch("/api/emby/library-mappings/{id}", d.UpdateEmbyLibraryMapping)
	r.Get("/api/account-pools", d.ListAccountPoolAssignments)
	r.Post("/api/account-pools", d.UpsertAccountPoolAssignment)
	r.Patch("/api/account-pools/{id}", d.UpsertAccountPoolAssignment)
	r.Get("/api/quota/policies", d.ListQuotaPolicies)
	r.Post("/api/quota/policies", d.CreateQuotaPolicy)
	r.Patch("/api/quota/policies/{id}", d.UpdateQuotaPolicy)
	r.Get("/api/playback/sessions", d.ListPlaybackSessions)
	r.Get("/api/playback/events", d.ListPlaybackEvents)
	r.Get("/api/quota/usage", d.GetQuotaUsage)
}

// requireAdmin returns the request's identity only when it carries the admin scope;
// otherwise it writes 403 and returns ok=false. It gates every management write and
// the cross-user admin reads.
func requireAdmin(w http.ResponseWriter, r *http.Request) (auth.UserContext, bool) {
	user := auth.FromContext(r.Context())
	if !user.HasScope("admin") {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return auth.UserContext{}, false
	}
	return user, true
}

// requestedUserOrSelf resolves the echo_user_id an owner-scoped read targets: the
// caller itself when no ?user_id= is given, the requested id when it matches the
// caller, and (only) for admins any requested id. A non-admin asking for another
// user's data gets 403.
func requestedUserOrSelf(w http.ResponseWriter, r *http.Request) (string, bool) {
	user := auth.FromContext(r.Context())
	requested := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if requested == "" {
		return user.UserID, true
	}
	if requested != user.UserID && !user.HasScope("admin") {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return "", false
	}
	return requested, true
}

// boolToInt maps a JSON bool onto the INTEGER 0/1 the SQLite schema stores for the
// enabled / case_sensitive flags.
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// nullInt64Ptr wraps an optional int64 body field as a NULL-able column value.
func nullInt64Ptr(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

// --- emby_servers ---

type embyServerRequest struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	BaseURL       string `json:"base_url"`
	APIKeyRef     string `json:"api_key_ref"`
	PublicBaseURL string `json:"public_base_url"`
	ProxyPrefix   string `json:"proxy_prefix"`
	Enabled       bool   `json:"enabled"`
}

// ListEmbyServers serves GET /api/emby/servers (admin). The list view omits
// api_key_ref: it is a secret reference, never echoed back.
func (d APIDeps) ListEmbyServers(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	rows, err := d.Store.ListEmbyServers(r.Context(), queries.ListEmbyServersParams{
		Limit:  queryLimit(r, 100, 500),
		Offset: queryOffset(r),
	})
	if err != nil {
		d.logger().Error("emby servers: list", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	if rows == nil {
		rows = []queries.ListEmbyServersRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// UpsertEmbyServer serves POST /api/emby/servers and PATCH /api/emby/servers/{id}
// (admin). The string PK is supplied in the body; on PATCH the route id must match
// it. api_key_ref is accepted and persisted but never reflected in the response,
// which uses the same api_key_ref-free row shape as the list.
func (d APIDeps) UpsertEmbyServer(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var req embyServerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if routeID := chi.URLParam(r, "id"); routeID != "" && routeID != id {
		writeAPIError(w, http.StatusBadRequest, "route id must match body id")
		return
	}
	if id == "" || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.BaseURL) == "" || strings.TrimSpace(req.PublicBaseURL) == "" {
		writeAPIError(w, http.StatusBadRequest, "id, name, base_url and public_base_url are required")
		return
	}
	proxyPrefix := req.ProxyPrefix
	if proxyPrefix == "" {
		proxyPrefix = "/emby"
	}
	now := d.now().Unix()
	row, err := d.Store.UpsertEmbyServer(r.Context(), queries.UpsertEmbyServerParams{
		ID:            id,
		Name:          req.Name,
		BaseUrl:       req.BaseURL,
		ApiKeyRef:     sql.NullString{String: req.APIKeyRef, Valid: req.APIKeyRef != ""},
		PublicBaseUrl: req.PublicBaseURL,
		ProxyPrefix:   proxyPrefix,
		Enabled:       boolToInt(req.Enabled),
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		d.logger().Error("emby servers: upsert", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// --- emby_user_links ---

type embyUserLinkRequest struct {
	EmbyServerID string `json:"emby_server_id"`
	EmbyUserID   string `json:"emby_user_id"`
	EchoUserID   string `json:"echo_user_id"`
	Enabled      bool   `json:"enabled"`
}

// ListEmbyUserLinks serves GET /api/emby/user-links (admin).
func (d APIDeps) ListEmbyUserLinks(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	rows, err := d.Store.ListEmbyUserLinks(r.Context(), queries.ListEmbyUserLinksParams{
		Limit:  queryLimit(r, 100, 500),
		Offset: queryOffset(r),
	})
	if err != nil {
		d.logger().Error("emby user-links: list", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	if rows == nil {
		rows = []queries.ListEmbyUserLinksRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// UpsertEmbyUserLink serves POST /api/emby/user-links and PATCH
// /api/emby/user-links/{id} (admin). The (emby_server_id, emby_user_id) pair is the
// natural key the upsert resolves on, so PATCH-by-id is NOT an id-targeted update
// here: the numeric route id is informational only and is intentionally not enforced
// against the body — the row updated is the one matching the body's composite key.
func (d APIDeps) UpsertEmbyUserLink(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var req embyUserLinkRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.EmbyServerID) == "" || strings.TrimSpace(req.EmbyUserID) == "" || strings.TrimSpace(req.EchoUserID) == "" {
		writeAPIError(w, http.StatusBadRequest, "emby_server_id, emby_user_id and echo_user_id are required")
		return
	}
	now := d.now().Unix()
	row, err := d.Store.UpsertEmbyUserLink(r.Context(), queries.UpsertEmbyUserLinkParams{
		EmbyServerID: req.EmbyServerID,
		EmbyUserID:   req.EmbyUserID,
		EchoUserID:   req.EchoUserID,
		Enabled:      boolToInt(req.Enabled),
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		d.logger().Error("emby user-links: upsert", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// --- emby_library_mappings ---

type embyLibraryMappingRequest struct {
	EmbyServerID   string `json:"emby_server_id"`
	EmbyPathPrefix string `json:"emby_path_prefix"`
	LibraryID      int64  `json:"library_id"`
	EchoRelPrefix  string `json:"echo_rel_prefix"`
	CaseSensitive  bool   `json:"case_sensitive"`
	Enabled        bool   `json:"enabled"`
}

// ListEmbyLibraryMappings serves GET /api/emby/library-mappings (admin).
func (d APIDeps) ListEmbyLibraryMappings(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	rows, err := d.Store.ListEmbyLibraryMappings(r.Context(), queries.ListEmbyLibraryMappingsParams{
		Limit:  queryLimit(r, 100, 500),
		Offset: queryOffset(r),
	})
	if err != nil {
		d.logger().Error("emby library-mappings: list", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	if rows == nil {
		rows = []queries.ListEmbyLibraryMappingsRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// CreateEmbyLibraryMapping serves POST /api/emby/library-mappings (admin). It
// derives emby_path_prefix_norm with embyproxy.NormalizeEmbyPath (the same security
// boundary used at match time) before persisting via the Phase 4 insert query.
func (d APIDeps) CreateEmbyLibraryMapping(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var req embyLibraryMappingRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.EmbyServerID) == "" || req.LibraryID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "emby_server_id and library_id are required")
		return
	}
	norm, err := embyproxy.NormalizeEmbyPath(req.EmbyPathPrefix)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid emby_path_prefix")
		return
	}
	now := d.now().Unix()
	row, err := d.Store.CreateEmbyLibraryMapping(r.Context(), queries.CreateEmbyLibraryMappingParams{
		EmbyServerID:       req.EmbyServerID,
		LibraryID:          req.LibraryID,
		EmbyPathPrefix:     req.EmbyPathPrefix,
		EmbyPathPrefixNorm: norm,
		EchoRelPrefix:      req.EchoRelPrefix,
		CaseSensitive:      boolToInt(req.CaseSensitive),
		Enabled:            boolToInt(req.Enabled),
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	if err != nil {
		d.logger().Error("emby library-mappings: create", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// UpdateEmbyLibraryMapping serves PATCH /api/emby/library-mappings/{id} (admin). The
// numeric route id is authoritative; the body carries the new field values (reusing
// the create shape minus the immutable emby_server_id).
func (d APIDeps) UpdateEmbyLibraryMapping(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok {
		return
	}
	var req embyLibraryMappingRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.LibraryID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "library_id is required")
		return
	}
	norm, err := embyproxy.NormalizeEmbyPath(req.EmbyPathPrefix)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid emby_path_prefix")
		return
	}
	row, err := d.Store.UpdateEmbyLibraryMapping(r.Context(), queries.UpdateEmbyLibraryMappingParams{
		EmbyPathPrefix:     req.EmbyPathPrefix,
		EmbyPathPrefixNorm: norm,
		LibraryID:          req.LibraryID,
		EchoRelPrefix:      req.EchoRelPrefix,
		CaseSensitive:      boolToInt(req.CaseSensitive),
		Enabled:            boolToInt(req.Enabled),
		UpdatedAt:          d.now().Unix(),
		ID:                 id,
	})
	if err != nil {
		d.logger().Error("emby library-mappings: update", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// --- account_pool_assignments ---

type accountPoolRequest struct {
	EchoUserID           string `json:"echo_user_id"`
	AccountID            string `json:"account_id"`
	Provider             string `json:"provider"`
	Priority             int64  `json:"priority"`
	Weight               int64  `json:"weight"`
	MaxConcurrentStreams *int64 `json:"max_concurrent_streams"`
	DailyBytesLimit      *int64 `json:"daily_bytes_limit"`
	Enabled              bool   `json:"enabled"`
}

// ListAccountPoolAssignments serves GET /api/account-pools (admin), optionally
// filtered by ?user_id=.
func (d APIDeps) ListAccountPoolAssignments(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	rows, err := d.Store.ListAccountPoolAssignments(r.Context(), queries.ListAccountPoolAssignmentsParams{
		Column1:    userID,
		EchoUserID: userID,
		Limit:      queryLimit(r, 100, 500),
		Offset:     queryOffset(r),
	})
	if err != nil {
		d.logger().Error("account-pools: list", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	if rows == nil {
		rows = []queries.AccountPoolAssignment{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// UpsertAccountPoolAssignment serves POST /api/account-pools and PATCH
// /api/account-pools/{id} (admin). The (echo_user_id, account_id) pair is the
// natural key the upsert resolves on, so PATCH-by-id is NOT an id-targeted update
// here: the numeric route id is informational only and is intentionally not enforced
// against the body — the row updated is the one matching the body's composite key.
func (d APIDeps) UpsertAccountPoolAssignment(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var req accountPoolRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.EchoUserID) == "" || strings.TrimSpace(req.AccountID) == "" || strings.TrimSpace(req.Provider) == "" {
		writeAPIError(w, http.StatusBadRequest, "echo_user_id, account_id and provider are required")
		return
	}
	now := d.now().Unix()
	row, err := d.Store.UpsertAccountPoolAssignment(r.Context(), queries.UpsertAccountPoolAssignmentParams{
		EchoUserID:           req.EchoUserID,
		AccountID:            req.AccountID,
		Provider:             req.Provider,
		Priority:             req.Priority,
		Weight:               req.Weight,
		MaxConcurrentStreams: nullInt64Ptr(req.MaxConcurrentStreams),
		DailyBytesLimit:      nullInt64Ptr(req.DailyBytesLimit),
		Enabled:              boolToInt(req.Enabled),
		CreatedAt:            now,
		UpdatedAt:            now,
	})
	if err != nil {
		d.logger().Error("account-pools: upsert", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// --- quota_policies ---

type quotaPolicyRequest struct {
	Name                string `json:"name"`
	Period              string `json:"period"`
	MaxBytes            *int64 `json:"max_bytes"`
	MaxStreams          *int64 `json:"max_streams"`
	MaxPlaybackSessions *int64 `json:"max_playback_sessions"`
}

// ListQuotaPolicies serves GET /api/quota/policies (admin).
func (d APIDeps) ListQuotaPolicies(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	rows, err := d.Store.ListQuotaPolicies(r.Context(), queries.ListQuotaPoliciesParams{
		Limit:  queryLimit(r, 100, 500),
		Offset: queryOffset(r),
	})
	if err != nil {
		d.logger().Error("quota policies: list", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	if rows == nil {
		rows = []queries.QuotaPolicy{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// CreateQuotaPolicy serves POST /api/quota/policies (admin).
func (d APIDeps) CreateQuotaPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var req quotaPolicyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Period) == "" {
		writeAPIError(w, http.StatusBadRequest, "name and period are required")
		return
	}
	now := d.now().Unix()
	row, err := d.Store.CreateQuotaPolicy(r.Context(), queries.CreateQuotaPolicyParams{
		Name:                req.Name,
		Period:              req.Period,
		MaxBytes:            nullInt64Ptr(req.MaxBytes),
		MaxStreams:          nullInt64Ptr(req.MaxStreams),
		MaxPlaybackSessions: nullInt64Ptr(req.MaxPlaybackSessions),
		CreatedAt:           now,
		UpdatedAt:           now,
	})
	if err != nil {
		d.logger().Error("quota policies: create", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// UpdateQuotaPolicy serves PATCH /api/quota/policies/{id} (admin). The numeric route
// id is authoritative; the query bumps version on each update.
func (d APIDeps) UpdateQuotaPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok {
		return
	}
	var req quotaPolicyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Period) == "" {
		writeAPIError(w, http.StatusBadRequest, "name and period are required")
		return
	}
	row, err := d.Store.UpdateQuotaPolicy(r.Context(), queries.UpdateQuotaPolicyParams{
		Name:                req.Name,
		Period:              req.Period,
		MaxBytes:            nullInt64Ptr(req.MaxBytes),
		MaxStreams:          nullInt64Ptr(req.MaxStreams),
		MaxPlaybackSessions: nullInt64Ptr(req.MaxPlaybackSessions),
		UpdatedAt:           d.now().Unix(),
		ID:                  id,
	})
	if err != nil {
		d.logger().Error("quota policies: update", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusOK, row)
}
