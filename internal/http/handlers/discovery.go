package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/discovery"
	"github.com/xmm2022/echo/internal/discovery/rules"
	"github.com/xmm2022/echo/internal/discovery/tmdb"
	"github.com/xmm2022/echo/internal/store/queries"
)

const (
	discoveryAPIDefaultLimit = 100
	discoveryAPIMaxLimit     = 500
	defaultTMDBLanguage      = "zh-CN"
	defaultTMDBRefreshTTL    = 24 * time.Hour
)

func (d APIDeps) MountDiscovery(r chi.Router) {
	r.Get("/api/discovery/subscriptions", d.ListDiscoverySubscriptions)
	r.Post("/api/discovery/subscriptions", d.CreateDiscoverySubscription)
	r.Patch("/api/discovery/subscriptions/{id}", d.UpdateDiscoverySubscription)
	r.Get("/api/discovery/tmdb/search", d.SearchDiscoveryTMDB)
	r.Get("/api/discovery/sources", d.ListDiscoverySources)
	r.Post("/api/discovery/sources", d.CreateDiscoverySource)
	r.Patch("/api/discovery/sources/{id}", d.UpdateDiscoverySource)
	r.Get("/api/discovery/producer-profiles", d.ListDiscoveryProducerProfiles)
	r.Post("/api/discovery/producer-profiles", d.CreateDiscoveryProducerProfile)
	r.Patch("/api/discovery/producer-profiles/{id}", d.UpdateDiscoveryProducerProfile)
	r.Get("/api/discovery/rule-profiles", d.ListDiscoveryRuleProfiles)
	r.Post("/api/discovery/rule-profiles", d.CreateDiscoveryRuleProfile)
	r.Patch("/api/discovery/rule-profiles/{id}", d.UpdateDiscoveryRuleProfile)
	r.Post("/api/discovery/rule-profiles/{id}/test", d.TestDiscoveryRuleProfile)
	r.Get("/api/discovery/candidates", d.ListDiscoveryCandidates)
	r.Get("/api/discovery/matches", d.ListDiscoveryMatches)
	r.Post("/api/discovery/matches/{id}/accept", d.AcceptDiscoveryMatch)
	r.Post("/api/discovery/matches/{id}/reject", d.RejectDiscoveryMatch)
	r.Post("/api/discovery/matches/{id}/retry", d.RetryDiscoveryMatch)
	r.Post("/api/discovery/run/source/{id}", d.RunDiscoverySource)
	r.Post("/api/discovery/run/subscription/{id}", d.RunDiscoverySubscription)
	r.Get("/api/discovery/debug/resources/{id}/raw", d.GetDiscoveryRawResource)
}

func (d APIDeps) ListDiscoverySubscriptions(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if !d.requireDiscoveryStore(w) {
		return
	}
	rows, err := d.Store.ListDiscoverySubscriptions(r.Context(), queries.ListDiscoverySubscriptionsParams{
		Limit:  queryLimit(r, discoveryAPIDefaultLimit, discoveryAPIMaxLimit),
		Offset: queryOffset(r),
	})
	if err != nil {
		d.writeDiscoveryError(w, "list discovery subscriptions", err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (d APIDeps) CreateDiscoverySubscription(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if !d.requireDiscoveryStore(w) {
		return
	}
	var req discoverySubscriptionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.normalize(user.UserID)
	if err := validateDiscoverySubscriptionRequest(req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if d.DiscoveryTMDB == nil {
		writeAPIError(w, http.StatusBadGateway, "tmdb client not configured")
		return
	}
	media, err := d.fetchDiscoveryTMDBDetails(r.Context(), req.MediaType, req.TMDBID)
	if err != nil {
		d.writeDiscoveryError(w, "fetch tmdb details", err)
		return
	}
	if err := d.upsertDiscoveryTMDBMedia(r.Context(), media, req.TMDBLanguage); err != nil {
		d.writeDiscoveryError(w, "upsert tmdb media", err)
		return
	}
	title := media.Title
	if media.ReleaseYear > 0 {
		title = title + " (" + strconvInt(media.ReleaseYear) + ")"
	}
	now := d.now().Unix()
	row, err := d.Store.CreateDiscoverySubscription(r.Context(), queries.CreateDiscoverySubscriptionParams{
		OwnerID:           req.OwnerID,
		TmdbID:            media.TMDBID,
		MediaType:         req.MediaType,
		TmdbLanguage:      req.TMDBLanguage,
		TitleSnapshot:     title,
		LibraryID:         req.LibraryID,
		ProducerProfileID: req.ProducerProfileID,
		RuleProfileID:     req.RuleProfileID,
		Status:            req.Status,
		SeasonFilterJson:  nullDiscoveryString(req.SeasonFilterJSON),
		NextCheckAt:       nullDiscoveryInt64(req.NextCheckAt),
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	if err != nil {
		d.writeDiscoveryError(w, "create discovery subscription", err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (d APIDeps) UpdateDiscoverySubscription(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok || !d.requireDiscoveryStore(w) {
		return
	}
	var req discoverySubscriptionUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Status = strings.TrimSpace(req.Status)
	if req.Status == "" {
		req.Status = "active"
	}
	if err := validateDiscoverySubscriptionUpdateRequest(req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := d.Store.UpdateDiscoverySubscription(r.Context(), queries.UpdateDiscoverySubscriptionParams{
		LibraryID:         req.LibraryID,
		ProducerProfileID: req.ProducerProfileID,
		RuleProfileID:     req.RuleProfileID,
		Status:            strings.TrimSpace(req.Status),
		SeasonFilterJson:  nullDiscoveryString(req.SeasonFilterJSON),
		NextCheckAt:       nullDiscoveryInt64(req.NextCheckAt),
		UpdatedAt:         d.now().Unix(),
		ID:                id,
	})
	if err != nil {
		d.writeDiscoveryError(w, "update discovery subscription", err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (d APIDeps) SearchDiscoveryTMDB(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	mediaType := strings.TrimSpace(r.URL.Query().Get("type"))
	if query == "" || !validDiscoveryMediaType(mediaType) {
		writeAPIError(w, http.StatusBadRequest, "invalid tmdb search")
		return
	}
	if d.DiscoveryTMDB == nil {
		writeAPIError(w, http.StatusBadGateway, "tmdb client not configured")
		return
	}
	results, err := d.DiscoveryTMDB.Search(r.Context(), query, mediaType)
	if err != nil {
		d.writeDiscoveryError(w, "search tmdb", err)
		return
	}
	writeJSON(w, http.StatusOK, discoveryTMDBSummaries(results))
}

func (d APIDeps) ListDiscoverySources(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if !d.requireDiscoveryStore(w) {
		return
	}
	rows, err := d.Store.ListDiscoverySources(r.Context(), queries.ListDiscoverySourcesParams{
		Limit:  queryLimit(r, discoveryAPIDefaultLimit, discoveryAPIMaxLimit),
		Offset: queryOffset(r),
	})
	if err != nil {
		d.writeDiscoveryError(w, "list discovery sources", err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (d APIDeps) CreateDiscoverySource(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if !d.requireDiscoveryStore(w) {
		return
	}
	var req discoverySourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateDiscoverySourceRequest(req, true); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := d.now().Unix()
	row, err := d.Store.CreateDiscoverySource(r.Context(), queries.CreateDiscoverySourceParams{
		Kind:          strings.TrimSpace(req.Kind),
		Name:          strings.TrimSpace(req.Name),
		Enabled:       boolToInt(req.Enabled),
		ConfigJson:    defaultJSON(req.ConfigJSON),
		SecretRef:     nullDiscoveryString(req.SecretRef),
		RateLimitJson: nullDiscoveryString(req.RateLimitJSON),
		NextRunAt:     nullDiscoveryInt64(req.NextRunAt),
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		d.writeDiscoveryError(w, "create discovery source", err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (d APIDeps) UpdateDiscoverySource(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok || !d.requireDiscoveryStore(w) {
		return
	}
	var req discoverySourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateDiscoverySourceRequest(req, false); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := d.Store.UpdateDiscoverySource(r.Context(), queries.UpdateDiscoverySourceParams{
		Name:          strings.TrimSpace(req.Name),
		Enabled:       boolToInt(req.Enabled),
		ConfigJson:    defaultJSON(req.ConfigJSON),
		SecretRef:     nullDiscoveryString(req.SecretRef),
		RateLimitJson: nullDiscoveryString(req.RateLimitJSON),
		UpdatedAt:     d.now().Unix(),
		ID:            id,
	})
	if err != nil {
		d.writeDiscoveryError(w, "update discovery source", err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (d APIDeps) ListDiscoveryProducerProfiles(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if !d.requireDiscoveryStore(w) {
		return
	}
	rows, err := d.Store.ListDiscoveryProducerProfiles(r.Context(), queries.ListDiscoveryProducerProfilesParams{
		Limit:  queryLimit(r, discoveryAPIDefaultLimit, discoveryAPIMaxLimit),
		Offset: queryOffset(r),
	})
	if err != nil {
		d.writeDiscoveryError(w, "list discovery producer profiles", err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (d APIDeps) CreateDiscoveryProducerProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if !d.requireDiscoveryStore(w) {
		return
	}
	var req producerProfileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateDiscoveryProducerProfileRequest(req, true); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := d.now().Unix()
	row, err := d.Store.CreateDiscoveryProducerProfile(r.Context(), queries.CreateDiscoveryProducerProfileParams{
		Name:                   strings.TrimSpace(req.Name),
		Provider:               strings.TrimSpace(req.Provider),
		Tool:                   strings.TrimSpace(req.Tool),
		TargetAccount:          strings.TrimSpace(req.TargetAccount),
		TargetSubdirTemplate:   strings.TrimSpace(req.TargetSubdirTemplate),
		LibraryRelPathTemplate: strings.TrimSpace(req.LibraryRelPathTemplate),
		DefaultArgsJson:        defaultJSON(req.DefaultArgsJSON),
		Enabled:                boolToInt(req.Enabled),
		CreatedAt:              now,
		UpdatedAt:              now,
	})
	if err != nil {
		d.writeDiscoveryError(w, "create discovery producer profile", err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (d APIDeps) UpdateDiscoveryProducerProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok || !d.requireDiscoveryStore(w) {
		return
	}
	var req producerProfileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateDiscoveryProducerProfileRequest(req, false); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := d.Store.UpdateDiscoveryProducerProfile(r.Context(), queries.UpdateDiscoveryProducerProfileParams{
		Name:                   strings.TrimSpace(req.Name),
		TargetAccount:          strings.TrimSpace(req.TargetAccount),
		TargetSubdirTemplate:   strings.TrimSpace(req.TargetSubdirTemplate),
		LibraryRelPathTemplate: strings.TrimSpace(req.LibraryRelPathTemplate),
		DefaultArgsJson:        defaultJSON(req.DefaultArgsJSON),
		Enabled:                boolToInt(req.Enabled),
		UpdatedAt:              d.now().Unix(),
		ID:                     id,
	})
	if err != nil {
		d.writeDiscoveryError(w, "update discovery producer profile", err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (d APIDeps) ListDiscoveryRuleProfiles(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if !d.requireDiscoveryStore(w) {
		return
	}
	rows, err := d.Store.ListRuleProfiles(r.Context(), queries.ListRuleProfilesParams{
		Limit:  queryLimit(r, discoveryAPIDefaultLimit, discoveryAPIMaxLimit),
		Offset: queryOffset(r),
	})
	if err != nil {
		d.writeDiscoveryError(w, "list discovery rule profiles", err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (d APIDeps) CreateDiscoveryRuleProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if !d.requireDiscoveryStore(w) {
		return
	}
	var req ruleProfileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := rules.ParseProfileJSON([]byte(req.RulesJSON)); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid rules_json")
		return
	}
	now := d.now().Unix()
	row, err := d.Store.CreateRuleProfile(r.Context(), queries.CreateRuleProfileParams{
		Name:      strings.TrimSpace(req.Name),
		Version:   1,
		RulesJson: req.RulesJSON,
		Enabled:   boolToInt(req.Enabled),
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		d.writeDiscoveryError(w, "create discovery rule profile", err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (d APIDeps) UpdateDiscoveryRuleProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok || !d.requireDiscoveryStore(w) {
		return
	}
	var req ruleProfileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := rules.ParseProfileJSON([]byte(req.RulesJSON)); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid rules_json")
		return
	}
	row, err := d.Store.UpdateRuleProfile(r.Context(), queries.UpdateRuleProfileParams{
		Name:      strings.TrimSpace(req.Name),
		RulesJson: req.RulesJSON,
		Enabled:   boolToInt(req.Enabled),
		UpdatedAt: d.now().Unix(),
		ID:        id,
	})
	if err != nil {
		d.writeDiscoveryError(w, "update discovery rule profile", err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (d APIDeps) TestDiscoveryRuleProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok || !d.requireDiscoveryStore(w) {
		return
	}
	var req ruleProfileTestRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	rulesJSON := strings.TrimSpace(req.RulesJSON)
	if rulesJSON == "" {
		row, err := d.Store.GetRuleProfile(r.Context(), queries.GetRuleProfileParams{ID: id})
		if err != nil {
			d.writeDiscoveryError(w, "get discovery rule profile", err)
			return
		}
		rulesJSON = row.RulesJson
	}
	profile, err := rules.ParseProfileJSON([]byte(rulesJSON))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid rules_json")
		return
	}
	score, decision := rules.Score(rules.ParseFeatures(req.Title, req.RawText), profile)
	writeJSON(w, http.StatusOK, map[string]any{"score": score, "decision": decision})
}

func (d APIDeps) ListDiscoveryCandidates(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if !d.requireDiscoveryStore(w) {
		return
	}
	rows, err := d.Store.ListDiscoveredResourcesRedacted(r.Context(), queries.ListDiscoveredResourcesRedactedParams{
		Limit:  queryLimit(r, discoveryAPIDefaultLimit, discoveryAPIMaxLimit),
		Offset: queryOffset(r),
	})
	if err != nil {
		d.writeDiscoveryError(w, "list discovery candidates", err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (d APIDeps) ListDiscoveryMatches(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if !d.requireDiscoveryStore(w) {
		return
	}
	rows, err := d.Store.ListSubscriptionMatchesForAdmin(r.Context(), queries.ListSubscriptionMatchesForAdminParams{
		Limit:  queryLimit(r, discoveryAPIDefaultLimit, discoveryAPIMaxLimit),
		Offset: queryOffset(r),
	})
	if err != nil {
		d.writeDiscoveryError(w, "list discovery matches", err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (d APIDeps) AcceptDiscoveryMatch(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok || !d.requireDiscoveryStore(w) || !d.requireDiscoveryJobs(w) {
		return
	}
	if err := discovery.NewStore(d.Store).AdminAcceptMatch(r.Context(), id, d.now()); err != nil {
		d.writeDiscoveryError(w, "accept discovery match", err)
		return
	}
	jobID, err := d.Jobs.Enqueue(r.Context(), discovery.KindDispatch, discovery.DispatchPayload{MatchID: id})
	if err != nil {
		d.writeDiscoveryError(w, "enqueue discovery dispatch", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int64{"job_id": jobID})
}

func (d APIDeps) RejectDiscoveryMatch(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok || !d.requireDiscoveryStore(w) {
		return
	}
	if err := discovery.NewStore(d.Store).AdminRejectMatch(r.Context(), id, d.now()); err != nil {
		d.writeDiscoveryError(w, "reject discovery match", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (d APIDeps) RetryDiscoveryMatch(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok || !d.requireDiscoveryStore(w) || !d.requireDiscoveryJobs(w) {
		return
	}
	if err := discovery.NewStore(d.Store).AdminRetryMatch(r.Context(), id, d.now()); err != nil {
		d.writeDiscoveryError(w, "retry discovery match", err)
		return
	}
	jobID, err := d.Jobs.Enqueue(r.Context(), discovery.KindDispatch, discovery.DispatchPayload{MatchID: id})
	if err != nil {
		d.writeDiscoveryError(w, "enqueue discovery dispatch", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int64{"job_id": jobID})
}

func (d APIDeps) RunDiscoverySource(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok || !d.requireDiscoveryJobs(w) {
		return
	}
	jobID, err := d.Jobs.Enqueue(r.Context(), discovery.KindSourceCrawl, discovery.SourceCrawlPayload{SourceID: id})
	if err != nil {
		d.writeDiscoveryError(w, "enqueue discovery source crawl", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int64{"job_id": jobID})
}

func (d APIDeps) RunDiscoverySubscription(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok || !d.requireDiscoveryJobs(w) {
		return
	}
	jobID, err := d.Jobs.Enqueue(r.Context(), discovery.KindSubscriptionCheck, discovery.SubscriptionCheckPayload{SubscriptionID: id})
	if err != nil {
		d.writeDiscoveryError(w, "enqueue discovery subscription check", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int64{"job_id": jobID})
}

func (d APIDeps) GetDiscoveryRawResource(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if !d.Config.DiscoveryRawDebug.Enabled {
		http.NotFound(w, r)
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok || !d.requireDiscoveryStore(w) {
		return
	}
	row, err := d.Store.GetDiscoveryRawResourceForDebug(r.Context(), queries.GetDiscoveryRawResourceForDebugParams{ID: id})
	if err != nil {
		d.writeDiscoveryError(w, "get discovery raw resource", err)
		return
	}
	if !row.RawTextRef.Valid || strings.TrimSpace(row.RawTextRef.String) == "" {
		http.NotFound(w, r)
		return
	}
	maxBytes := d.Config.DiscoveryRawDebug.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 4096
	}
	body, err := discovery.NewRawStore(discovery.RawStoreConfig{
		Root:     d.Config.DiscoveryRawDebug.StorageRoot,
		MaxBytes: maxBytes,
	}).Get(r.Context(), row.RawTextRef.String, maxBytes)
	if err != nil {
		d.writeDiscoveryError(w, "load discovery raw resource", err)
		return
	}
	redacted := discovery.RedactRawTextForDebug(string(body), maxBytes)
	if err := d.Store.CreateDiscoveryRawAccessEvent(r.Context(), queries.CreateDiscoveryRawAccessEventParams{
		ResourceID:    id,
		ActorUserID:   user.UserID,
		RequestID:     sql.NullString{String: strings.TrimSpace(r.Header.Get("X-Request-ID")), Valid: strings.TrimSpace(r.Header.Get("X-Request-ID")) != ""},
		ResponseBytes: int64(len([]byte(redacted))),
		Redacted:      1,
		AccessedAt:    d.now().Unix(),
	}); err != nil {
		d.writeDiscoveryError(w, "audit discovery raw resource", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resource_id": row.ID, "raw_text_redacted": redacted})
}

type discoverySubscriptionRequest struct {
	OwnerID           string `json:"owner_id"`
	TMDBID            string `json:"tmdb_id"`
	MediaType         string `json:"media_type"`
	TMDBLanguage      string `json:"tmdb_language"`
	LibraryID         int64  `json:"library_id"`
	ProducerProfileID int64  `json:"producer_profile_id"`
	RuleProfileID     int64  `json:"rule_profile_id"`
	Status            string `json:"status"`
	SeasonFilterJSON  string `json:"season_filter_json"`
	NextCheckAt       *int64 `json:"next_check_at"`
}

func (r *discoverySubscriptionRequest) normalize(defaultOwner string) {
	r.OwnerID = strings.TrimSpace(r.OwnerID)
	if r.OwnerID == "" {
		r.OwnerID = defaultOwner
	}
	r.TMDBID = strings.TrimSpace(r.TMDBID)
	r.MediaType = strings.TrimSpace(r.MediaType)
	r.TMDBLanguage = strings.TrimSpace(r.TMDBLanguage)
	if r.TMDBLanguage == "" {
		r.TMDBLanguage = defaultTMDBLanguage
	}
	r.Status = strings.TrimSpace(r.Status)
	if r.Status == "" {
		r.Status = "active"
	}
}

type discoverySubscriptionUpdateRequest struct {
	LibraryID         int64  `json:"library_id"`
	ProducerProfileID int64  `json:"producer_profile_id"`
	RuleProfileID     int64  `json:"rule_profile_id"`
	Status            string `json:"status"`
	SeasonFilterJSON  string `json:"season_filter_json"`
	NextCheckAt       *int64 `json:"next_check_at"`
}

type discoverySourceRequest struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	ConfigJSON    string `json:"config_json"`
	SecretRef     string `json:"secret_ref"`
	RateLimitJSON string `json:"rate_limit_json"`
	NextRunAt     *int64 `json:"next_run_at"`
}

type producerProfileRequest struct {
	Name                   string `json:"name"`
	Provider               string `json:"provider"`
	Tool                   string `json:"tool"`
	TargetAccount          string `json:"target_account"`
	TargetSubdirTemplate   string `json:"target_subdir_template"`
	LibraryRelPathTemplate string `json:"library_rel_path_template"`
	DefaultArgsJSON        string `json:"default_args_json"`
	Enabled                bool   `json:"enabled"`
}

type ruleProfileRequest struct {
	Name      string `json:"name"`
	RulesJSON string `json:"rules_json"`
	Enabled   bool   `json:"enabled"`
}

type ruleProfileTestRequest struct {
	RulesJSON string `json:"rules_json"`
	Title     string `json:"title"`
	RawText   string `json:"raw_text"`
}

type discoveryTMDBSummary struct {
	TMDBID        string `json:"tmdb_id"`
	MediaType     string `json:"media_type"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseYear   int    `json:"release_year"`
	PosterPath    string `json:"poster_path"`
}

func discoveryTMDBSummaries(rows []tmdb.Media) []discoveryTMDBSummary {
	out := make([]discoveryTMDBSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, discoveryTMDBSummary{
			TMDBID:        row.TMDBID,
			MediaType:     row.MediaType,
			Title:         row.Title,
			OriginalTitle: row.OriginalTitle,
			ReleaseYear:   row.ReleaseYear,
			PosterPath:    row.PosterPath,
		})
	}
	return out
}

func (d APIDeps) fetchDiscoveryTMDBDetails(ctx context.Context, mediaType, tmdbID string) (tmdb.Media, error) {
	switch mediaType {
	case "movie":
		return d.DiscoveryTMDB.MovieDetails(ctx, tmdbID)
	case "tv":
		return d.DiscoveryTMDB.TVDetails(ctx, tmdbID)
	default:
		return tmdb.Media{}, errors.New("unsupported media type")
	}
}

func (d APIDeps) upsertDiscoveryTMDBMedia(ctx context.Context, media tmdb.Media, language string) error {
	now := d.now()
	raw := media.RawJSON
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	_, err := d.Store.UpsertTMDBMedia(ctx, queries.UpsertTMDBMediaParams{
		TmdbID:        media.TMDBID,
		MediaType:     media.MediaType,
		Language:      language,
		Title:         media.Title,
		OriginalTitle: nullDiscoveryString(media.OriginalTitle),
		ReleaseYear:   nullDiscoveryInt64FromInt(media.ReleaseYear),
		PosterPath:    nullDiscoveryString(media.PosterPath),
		Status:        sql.NullString{String: "fresh", Valid: true},
		RawJson:       raw,
		FetchedAt:     now.Unix(),
		NextRefreshAt: now.Add(defaultTMDBRefreshTTL).Unix(),
	})
	return err
}

func (d APIDeps) requireDiscoveryStore(w http.ResponseWriter) bool {
	if d.Store == nil {
		writeAPIError(w, http.StatusInternalServerError, "store not configured")
		return false
	}
	return true
}

func (d APIDeps) requireDiscoveryJobs(w http.ResponseWriter) bool {
	if d.Jobs == nil {
		writeAPIError(w, http.StatusInternalServerError, "jobs not configured")
		return false
	}
	return true
}

func (d APIDeps) writeDiscoveryError(w http.ResponseWriter, label string, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	if errors.Is(err, discovery.ErrAdminMatchNotFound) {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	if errors.Is(err, discovery.ErrInvalidAdminMatchTransition) {
		writeAPIError(w, http.StatusConflict, "invalid match transition")
		return
	}
	if isDiscoveryValidationError(err) {
		writeAPIError(w, http.StatusBadRequest, "invalid discovery input")
		return
	}
	d.logger().Error("discovery api: "+label, "err", err)
	writeAPIError(w, http.StatusInternalServerError, "internal error")
}

func validDiscoveryMediaType(value string) bool {
	return value == "movie" || value == "tv"
}

func validateDiscoverySubscriptionRequest(req discoverySubscriptionRequest) error {
	if req.TMDBID == "" || !validDiscoveryMediaType(req.MediaType) {
		return errors.New("invalid tmdb subscription")
	}
	if !validDiscoverySubscriptionStatus(req.Status) {
		return errors.New("invalid subscription status")
	}
	if req.LibraryID <= 0 || req.ProducerProfileID <= 0 || req.RuleProfileID <= 0 {
		return errors.New("invalid subscription references")
	}
	if err := validateOptionalJSONObject(req.SeasonFilterJSON, "season_filter_json"); err != nil {
		return err
	}
	return nil
}

func validateDiscoverySubscriptionUpdateRequest(req discoverySubscriptionUpdateRequest) error {
	if !validDiscoverySubscriptionStatus(req.Status) {
		return errors.New("invalid subscription status")
	}
	if req.LibraryID <= 0 || req.ProducerProfileID <= 0 || req.RuleProfileID <= 0 {
		return errors.New("invalid subscription references")
	}
	if err := validateOptionalJSONObject(req.SeasonFilterJSON, "season_filter_json"); err != nil {
		return err
	}
	return nil
}

func validateDiscoverySourceRequest(req discoverySourceRequest, create bool) error {
	if create && !validDiscoverySourceKind(strings.TrimSpace(req.Kind)) {
		return errors.New("invalid source kind")
	}
	if !create && strings.TrimSpace(req.Kind) != "" && !validDiscoverySourceKind(strings.TrimSpace(req.Kind)) {
		return errors.New("invalid source kind")
	}
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("source name is required")
	}
	if err := validateJSONObject(defaultJSON(req.ConfigJSON), "config_json"); err != nil {
		return err
	}
	if err := validateOptionalJSONObject(req.RateLimitJSON, "rate_limit_json"); err != nil {
		return err
	}
	return nil
}

func validateDiscoveryProducerProfileRequest(req producerProfileRequest, create bool) error {
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("producer profile name is required")
	}
	if create && strings.TrimSpace(req.Provider) != "115" {
		return errors.New("invalid producer provider")
	}
	if create && strings.TrimSpace(req.Tool) != "115share2cas" {
		return errors.New("invalid producer tool")
	}
	if strings.TrimSpace(req.TargetAccount) == "" {
		return errors.New("target_account is required")
	}
	return validateJSONObject(defaultJSON(req.DefaultArgsJSON), "default_args_json")
}

func validDiscoverySourceKind(value string) bool {
	switch value {
	case "telegram_mtproto", "poster_http", "manual":
		return true
	default:
		return false
	}
}

func validDiscoverySubscriptionStatus(value string) bool {
	switch value {
	case "active", "paused", "completed", "disabled":
		return true
	default:
		return false
	}
}

func validateOptionalJSONObject(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return validateJSONObject(value, field)
}

func validateJSONObject(value, field string) error {
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return errors.New("invalid " + field)
	}
	if _, ok := decoded.(map[string]any); !ok {
		return errors.New("invalid " + field)
	}
	return nil
}

func isDiscoveryValidationError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "constraint failed") || strings.Contains(msg, "constraint violation")
}

func defaultJSON(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "{}"
	}
	return value
}

func nullDiscoveryString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}

func nullDiscoveryInt64(value *int64) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *value, Valid: true}
}

func nullDiscoveryInt64FromInt(value int) sql.NullInt64 {
	if value <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(value), Valid: true}
}

func strconvInt(value int) string {
	return strconv.FormatInt(int64(value), 10)
}
