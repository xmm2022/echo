package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/media"
)

const (
	mediaAPIDefaultLimit = 50
	mediaAPIMaxLimit     = 100
)

type createMediaRequestBody struct {
	TMDBID           string `json:"tmdb_id"`
	MediaType        string `json:"media_type"`
	TMDBLanguage     string `json:"tmdb_language"`
	SeasonFilterJSON string `json:"season_filter_json"`
	TargetID         int64  `json:"target_id"`
	UserNote         string `json:"user_note"`
}

var createMediaRequestAllowedFields = map[string]struct{}{
	"tmdb_id":            {},
	"media_type":         {},
	"tmdb_language":      {},
	"season_filter_json": {},
	"target_id":          {},
	"user_note":          {},
}

func (d APIDeps) MountMedia(r chi.Router) {
	r.Get("/api/me/discovery/search", d.SearchMedia)
	r.Get("/api/me/discovery/catalog", d.ListMediaCatalog)
	r.Post("/api/me/discovery/requests", d.CreateMediaRequest)
	r.Get("/api/me/discovery/requests", d.ListMediaRequests)
	r.Get("/api/me/discovery/requests/{id}", d.GetMediaRequest)
	r.Post("/api/me/discovery/requests/{id}/cancel", d.CancelMediaRequest)
	r.Get("/api/me/discovery/subscriptions", d.ListMediaSubscriptions)
	r.Post("/api/me/discovery/subscriptions/{id}/pause", d.PauseMediaSubscription)
	r.Post("/api/me/discovery/subscriptions/{id}/resume", d.ResumeMediaSubscription)
}

func (d APIDeps) SearchMedia(w http.ResponseWriter, r *http.Request) {
	actor, ok := d.requireMediaService(w, r)
	if !ok {
		return
	}
	rows, err := d.Media.Search(r.Context(), actor, media.SearchInput{
		Query:     r.URL.Query().Get("q"),
		MediaType: r.URL.Query().Get("type"),
		Language:  r.URL.Query().Get("language"),
	})
	if err != nil {
		d.writeMediaError(w, err)
		return
	}
	if rows == nil {
		rows = []media.TMDBSummaryDTO{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (d APIDeps) ListMediaCatalog(w http.ResponseWriter, r *http.Request) {
	actor, ok := d.requireMediaService(w, r)
	if !ok {
		return
	}
	rows, err := d.Media.Catalog(r.Context(), actor)
	if err != nil {
		d.writeMediaError(w, err)
		return
	}
	if rows == nil {
		rows = []media.TargetDTO{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (d APIDeps) CreateMediaRequest(w http.ResponseWriter, r *http.Request) {
	actor, ok := d.requireMediaService(w, r)
	if !ok {
		return
	}
	body, ok := decodeCreateMediaRequestBody(w, r)
	if !ok {
		return
	}
	row, err := d.Media.CreateRequest(r.Context(), actor, media.CreateRequestInput{
		TMDBID:           body.TMDBID,
		MediaType:        body.MediaType,
		TMDBLanguage:     body.TMDBLanguage,
		SeasonFilterJSON: body.SeasonFilterJSON,
		TargetID:         body.TargetID,
		UserNote:         body.UserNote,
	})
	if err != nil {
		d.writeMediaError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (d APIDeps) ListMediaRequests(w http.ResponseWriter, r *http.Request) {
	actor, ok := d.requireMediaService(w, r)
	if !ok {
		return
	}
	rows, err := d.Media.ListRequests(r.Context(), actor, queryLimit(r, mediaAPIDefaultLimit, mediaAPIMaxLimit), queryOffset(r))
	if err != nil {
		d.writeMediaError(w, err)
		return
	}
	if rows == nil {
		rows = []media.RequestDTO{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (d APIDeps) GetMediaRequest(w http.ResponseWriter, r *http.Request) {
	actor, ok := d.requireMediaService(w, r)
	if !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok {
		return
	}
	row, err := d.Media.GetRequest(r.Context(), actor, id)
	if err != nil {
		d.writeMediaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (d APIDeps) CancelMediaRequest(w http.ResponseWriter, r *http.Request) {
	actor, ok := d.requireMediaService(w, r)
	if !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok {
		return
	}
	row, err := d.Media.CancelRequest(r.Context(), actor, id)
	if err != nil {
		d.writeMediaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (d APIDeps) ListMediaSubscriptions(w http.ResponseWriter, r *http.Request) {
	actor, ok := d.requireMediaService(w, r)
	if !ok {
		return
	}
	rows, err := d.Media.ListSubscriptions(r.Context(), actor, queryLimit(r, mediaAPIDefaultLimit, mediaAPIMaxLimit), queryOffset(r))
	if err != nil {
		d.writeMediaError(w, err)
		return
	}
	if rows == nil {
		rows = []media.SubscriptionDTO{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (d APIDeps) PauseMediaSubscription(w http.ResponseWriter, r *http.Request) {
	actor, ok := d.requireMediaService(w, r)
	if !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok {
		return
	}
	row, err := d.Media.PauseSubscription(r.Context(), actor, id)
	if err != nil {
		d.writeMediaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (d APIDeps) ResumeMediaSubscription(w http.ResponseWriter, r *http.Request) {
	actor, ok := d.requireMediaService(w, r)
	if !ok {
		return
	}
	id, ok := parseInt64Param(w, r, "id")
	if !ok {
		return
	}
	row, err := d.Media.ResumeSubscription(r.Context(), actor, id)
	if err != nil {
		d.writeMediaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (d APIDeps) requireMediaService(w http.ResponseWriter, r *http.Request) (media.Actor, bool) {
	w.Header().Set("Cache-Control", "no-store")
	user, ok := requireDiscoveryScope(w, r)
	if !ok {
		return media.Actor{}, false
	}
	if d.Media == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "media service not configured")
		return media.Actor{}, false
	}
	return media.Actor{User: user, IP: r.RemoteAddr}, true
}

func requireDiscoveryScope(w http.ResponseWriter, r *http.Request) (auth.UserContext, bool) {
	user := auth.FromContext(r.Context())
	if !user.HasScope("discovery") {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return auth.UserContext{}, false
	}
	return user, true
}

func decodeCreateMediaRequestBody(w http.ResponseWriter, r *http.Request) (createMediaRequestBody, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIBodyBytes)
	dec := json.NewDecoder(r.Body)
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return createMediaRequestBody{}, false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return createMediaRequestBody{}, false
	}
	for key := range raw {
		if _, ok := createMediaRequestAllowedFields[key]; !ok {
			writeAPIError(w, http.StatusBadRequest, "forbidden request field")
			return createMediaRequestBody{}, false
		}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return createMediaRequestBody{}, false
	}
	var body createMediaRequestBody
	if err := json.Unmarshal(data, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return createMediaRequestBody{}, false
	}
	return body, true
}

func (d APIDeps) writeMediaError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, media.ErrPolicyDenied):
		writeAPIError(w, http.StatusForbidden, "request-disabled")
	case errors.Is(err, media.ErrLimitReached):
		writeAPIError(w, http.StatusTooManyRequests, "request-limit-reached")
	case errors.Is(err, media.ErrMetadataUnavailable):
		writeAPIError(w, http.StatusServiceUnavailable, "metadata-unavailable")
	case errors.Is(err, media.ErrInvalidRequest):
		writeAPIError(w, http.StatusBadRequest, "invalid-request")
	case errors.Is(err, media.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not-found")
	case errors.Is(err, media.ErrInvalidTransition):
		writeAPIError(w, http.StatusConflict, "invalid-transition")
	case errors.Is(err, media.ErrConflict):
		writeAPIError(w, http.StatusConflict, "conflict")
	default:
		d.logger().Error("media api error", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal error")
	}
}
