package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/store/queries"
)

// playbackEventResponse is the flat JSON view of a playback_events row. The stored
// row carries several sql.Null* columns that marshal as {"String":..,"Valid":..}
// objects; the API flattens them (empty string / 0 / null) so clients read a plain
// "echo_user_id":"u1" rather than the driver's wrapper shape.
type playbackEventResponse struct {
	ID             int64   `json:"id"`
	RequestID      string  `json:"request_id"`
	SessionID      *string `json:"session_id"`
	ErrorTokenID   *string `json:"error_token_id"`
	EchoUserID     string  `json:"echo_user_id"`
	LibraryEntryID *int64  `json:"library_entry_id"`
	BlobID         *int64  `json:"blob_id"`
	CopyID         *int64  `json:"copy_id"`
	Provider       *string `json:"provider"`
	AccountID      *string `json:"account_id"`
	Operation      string  `json:"operation"`
	Status         string  `json:"status"`
	BytesSent      int64   `json:"bytes_sent"`
	RangeHeader    *string `json:"range_header"`
	HTTPStatus     *int64  `json:"http_status"`
	FailureKind    *string `json:"failure_kind"`
	FailureMessage *string `json:"failure_message"`
	StartedAt      int64   `json:"started_at"`
	FinishedAt     *int64  `json:"finished_at"`
}

func nullStrPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func nullIntPtr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func toPlaybackEventResponses(events []queries.PlaybackEvent) []playbackEventResponse {
	out := make([]playbackEventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, playbackEventResponse{
			ID:             e.ID,
			RequestID:      e.RequestID,
			SessionID:      nullStrPtr(e.SessionID),
			ErrorTokenID:   nullStrPtr(e.ErrorTokenID),
			EchoUserID:     e.EchoUserID.String,
			LibraryEntryID: nullIntPtr(e.LibraryEntryID),
			BlobID:         nullIntPtr(e.BlobID),
			CopyID:         nullIntPtr(e.CopyID),
			Provider:       nullStrPtr(e.Provider),
			AccountID:      nullStrPtr(e.AccountID),
			Operation:      e.Operation,
			Status:         e.Status,
			BytesSent:      e.BytesSent,
			RangeHeader:    nullStrPtr(e.RangeHeader),
			HTTPStatus:     nullIntPtr(e.HttpStatus),
			FailureKind:    nullStrPtr(e.FailureKind),
			FailureMessage: nullStrPtr(e.FailureMessage),
			StartedAt:      e.StartedAt,
			FinishedAt:     nullIntPtr(e.FinishedAt),
		})
	}
	return out
}

// playbackSessionResponse is the redacted JSON view of a playback_sessions row.
// It DELIBERATELY OMITS the live-token credential columns selector and token_hash
// (the plaintext selector half and the sha256 of the secret half): echoing them
// would let a management-API reader reconstruct or correlate usable playback
// tokens, the exact leak the split-token threat model in internal/embyproxy/session.go
// guards against. Like playbackEventResponse it also flattens the row's sql.Null*
// columns to plain JSON (string/number/null) instead of the driver's {String,Valid}
// wrapper shape.
type playbackSessionResponse struct {
	ID                string  `json:"id"`
	EchoUserID        string  `json:"echo_user_id"`
	EmbyServerID      string  `json:"emby_server_id"`
	EmbyUserID        string  `json:"emby_user_id"`
	DeviceID          *string `json:"device_id"`
	ItemID            string  `json:"item_id"`
	MediaSourceID     string  `json:"media_source_id"`
	EmbyPlaySessionID *string `json:"emby_play_session_id"`
	LibraryEntryID    *int64  `json:"library_entry_id"`
	BlobID            *int64  `json:"blob_id"`
	PreferProvider    *string `json:"prefer_provider"`
	State             string  `json:"state"`
	FailureReason     *string `json:"failure_reason"`
	CreatedAt         int64   `json:"created_at"`
	LastSeenAt        int64   `json:"last_seen_at"`
	ExpiresAt         int64   `json:"expires_at"`
}

func toPlaybackSessionResponses(sessions []queries.PlaybackSession) []playbackSessionResponse {
	out := make([]playbackSessionResponse, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, playbackSessionResponse{
			ID:                s.ID,
			EchoUserID:        s.EchoUserID,
			EmbyServerID:      s.EmbyServerID,
			EmbyUserID:        s.EmbyUserID,
			DeviceID:          nullStrPtr(s.DeviceID),
			ItemID:            s.ItemID,
			MediaSourceID:     s.MediaSourceID,
			EmbyPlaySessionID: nullStrPtr(s.EmbyPlaySessionID),
			LibraryEntryID:    nullIntPtr(s.LibraryEntryID),
			BlobID:            nullIntPtr(s.BlobID),
			PreferProvider:    nullStrPtr(s.PreferProvider),
			State:             s.State,
			FailureReason:     nullStrPtr(s.FailureReason),
			CreatedAt:         s.CreatedAt,
			LastSeenAt:        s.LastSeenAt,
			ExpiresAt:         s.ExpiresAt,
		})
	}
	return out
}

// ListPlaybackSessions serves GET /api/playback/sessions. It is owner-scoped:
// without ?user_id= a caller sees only their own sessions; an admin may pass
// ?user_id= to read any user's, and a non-admin asking for another user is denied
// by requestedUserOrSelf (403). When the resolved identity is the admin itself
// (no filter, admin caller) the admin-wide query returns every session.
func (d APIDeps) ListPlaybackSessions(w http.ResponseWriter, r *http.Request) {
	userID, ok := requestedUserOrSelf(w, r)
	if !ok {
		return
	}
	caller := auth.FromContext(r.Context())
	limit := queryLimit(r, 100, 500)
	offset := queryOffset(r)

	// An admin with no explicit ?user_id= filter reads across all users; everyone
	// else (and an admin filtering to a specific user) is scoped to userID.
	if caller.HasScope("admin") && userID == caller.UserID {
		rows, err := d.Store.ListPlaybackSessionsForAdmin(r.Context(), queries.ListPlaybackSessionsForAdminParams{
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			d.logger().Error("playback sessions: list admin", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "internal-error")
			return
		}
		writeJSON(w, http.StatusOK, toPlaybackSessionResponses(rows))
		return
	}

	rows, err := d.Store.ListPlaybackSessionsForUser(r.Context(), queries.ListPlaybackSessionsForUserParams{
		EchoUserID: userID,
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		d.logger().Error("playback sessions: list user", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusOK, toPlaybackSessionResponses(rows))
}

// ListPlaybackEvents serves GET /api/playback/events. Owner-scoped like
// ListPlaybackSessions: a non-admin only ever sees its own events; an admin reads
// across all users by default and may filter to one user via ?user_id=.
func (d APIDeps) ListPlaybackEvents(w http.ResponseWriter, r *http.Request) {
	userID, ok := requestedUserOrSelf(w, r)
	if !ok {
		return
	}
	caller := auth.FromContext(r.Context())
	limit := queryLimit(r, 100, 500)
	offset := queryOffset(r)

	if caller.HasScope("admin") {
		// userID is "" only when an admin passes no filter (requestedUserOrSelf
		// returns the caller's own id, never empty), so derive the filter from the
		// raw query: empty filter => all users, otherwise the requested user.
		filter := userID
		if userID == caller.UserID && r.URL.Query().Get("user_id") == "" {
			filter = ""
		}
		rows, err := d.Store.ListPlaybackEventsForAdmin(r.Context(), queries.ListPlaybackEventsForAdminParams{
			Column1:    filter,
			EchoUserID: sql.NullString{String: filter, Valid: filter != ""},
			Limit:      limit,
			Offset:     offset,
		})
		if err != nil {
			d.logger().Error("playback events: list admin", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "internal-error")
			return
		}
		writeJSON(w, http.StatusOK, toPlaybackEventResponses(rows))
		return
	}

	rows, err := d.Store.ListPlaybackEventsForUser(r.Context(), queries.ListPlaybackEventsForUserParams{
		EchoUserID: sql.NullString{String: userID, Valid: true},
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		d.logger().Error("playback events: list user", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusOK, toPlaybackEventResponses(rows))
}

// quotaUsageResponse is the GET /api/quota/usage view: the resolved user, the policy
// driving the window, the [period_start, period_end) the usage was summed over, and
// the bytes / stream count drawn from the authoritative playback_events table.
type quotaUsageResponse struct {
	UserID        string `json:"user_id"`
	QuotaPolicyID int64  `json:"quota_policy_id"`
	PolicyVersion int64  `json:"policy_version"`
	Period        string `json:"period"`
	PeriodStart   int64  `json:"period_start"`
	PeriodEnd     int64  `json:"period_end"`
	BytesUsed     int64  `json:"bytes_used"`
	StreamCount   int64  `json:"stream_count"`
}

// GetQuotaUsage serves GET /api/quota/usage. It is owner-scoped (requestedUserOrSelf:
// a non-admin asking for another user gets 403). Usage is computed from the Phase 2
// AUTHORITATIVE playback_events table via GetQuotaUsageFromEvents over the resolved
// user's quota-policy period window, NOT from the future/cache quota_usage table.
func (d APIDeps) GetQuotaUsage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requestedUserOrSelf(w, r)
	if !ok {
		return
	}
	user, err := d.Store.GetUser(r.Context(), queries.GetUserParams{ID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "user not found")
		return
	} else if err != nil {
		d.logger().Error("quota usage: get user", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	policy, err := d.Store.GetQuotaPolicy(r.Context(), queries.GetQuotaPolicyParams{ID: user.QuotaPolicyID})
	if err != nil {
		d.logger().Error("quota usage: get policy", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}

	start, end := quotaPeriodWindow(policy.Period, d.now())
	usage, err := d.Store.GetQuotaUsageFromEvents(r.Context(), queries.GetQuotaUsageFromEventsParams{
		EchoUserID:  sql.NullString{String: userID, Valid: true},
		StartedAt:   start,
		StartedAt_2: end,
	})
	if err != nil {
		d.logger().Error("quota usage: sum events", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}

	writeJSON(w, http.StatusOK, quotaUsageResponse{
		UserID:        userID,
		QuotaPolicyID: policy.ID,
		PolicyVersion: policy.Version,
		Period:        policy.Period,
		PeriodStart:   start,
		PeriodEnd:     end,
		BytesUsed:     coerceInt64(usage.BytesUsed),
		StreamCount:   usage.StreamCount,
	})
}

// quotaPeriodWindow returns the [start, end) Unix-second window the usage is summed
// over for a quota period. It mirrors the metered windows the playback quota enforcer
// uses (day = UTC midnight, month = first-of-month UTC, rolling_24h = now-24h..now).
// For the 'none' policy (and any unrecognized period) it returns the all-time window
// [0, now) so the reported usage still reflects every recorded event.
func quotaPeriodWindow(period string, now time.Time) (start, end int64) {
	u := now.UTC()
	switch period {
	case "day":
		s := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
		return s.Unix(), s.Add(24 * time.Hour).Unix()
	case "month":
		s := time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
		return s.Unix(), s.AddDate(0, 1, 0).Unix()
	case "rolling_24h":
		return now.Add(-24 * time.Hour).Unix(), now.Unix()
	default:
		return 0, now.Unix()
	}
}

// coerceInt64 normalizes the COALESCE(SUM(...)) result, which sqlc types as
// interface{}, into an int64. modernc.org/sqlite returns it as int64 for integer
// sums, but float64 / []byte / nil are handled defensively so a backend quirk never
// panics the handler.
func coerceInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case nil:
		return 0
	default:
		return 0
	}
}
