package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

// insertPlaybackSessionRow seeds a playback_sessions row carrying live token
// credential material (selector + token_hash) so tests can assert the API never
// echoes those secret columns back. It uses the 'expired' state so the row needs
// no library_entry_id/blob_id (the schema CHECK only requires those for 'active')
// and upserts the referenced emby_servers row to satisfy the FK.
func insertPlaybackSessionRow(t *testing.T, ctx context.Context, st *store.Store, id, userID, selector, tokenHash string) {
	t.Helper()
	if _, err := st.UpsertEmbyServer(ctx, queries.UpsertEmbyServerParams{
		ID:            "srv1",
		Name:          "srv1",
		BaseUrl:       "http://emby:8096",
		PublicBaseUrl: "https://echo.example.com",
		ProxyPrefix:   "/emby",
		Enabled:       1,
		CreatedAt:     1,
		UpdatedAt:     1,
	}); err != nil {
		t.Fatalf("seed emby server for session: %v", err)
	}
	if err := st.CreatePlaybackSession(ctx, queries.CreatePlaybackSessionParams{
		ID:            id,
		Selector:      selector,
		TokenHash:     tokenHash,
		EchoUserID:    userID,
		EmbyServerID:  "srv1",
		EmbyUserID:    "emby-" + userID,
		ItemID:        "item1",
		MediaSourceID: "ms1",
		State:         "expired",
		CreatedAt:     1000,
		LastSeenAt:    1000,
		ExpiresAt:     9000,
	}); err != nil {
		t.Fatalf("create playback session %s: %v", id, err)
	}
}

func TestPlaybackManagementOwnerScope(t *testing.T) {
	st := newAPIStore(t)
	deps := v02Deps(st)
	ctx := context.Background()
	createUserRow(t, st, "u1", "user")
	createUserRow(t, st, "u2", "user")
	insertPlaybackEventRow(t, ctx, st, "u1", "ok", 111)
	insertPlaybackEventRow(t, ctx, st, "u2", "quota_exceeded", 222)

	user := auth.UserContext{UserID: "u1", Role: "user", Scopes: []string{"read"}}
	rec := doReqAs(t, http.MethodGet, "/api/playback/events", "", user, func(r chi.Router) {
		deps.MountV02Management(r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("events status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"echo_user_id":"u1"`) || strings.Contains(body, `"echo_user_id":"u2"`) {
		t.Fatalf("owner-scoped body=%s", body)
	}

	rec = doReqAs(t, http.MethodGet, "/api/quota/usage?user_id=u2", "", user, func(r chi.Router) {
		deps.MountV02Management(r)
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-user quota status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
}

func TestAdminCanReadCrossUserPlaybackManagement(t *testing.T) {
	st := newAPIStore(t)
	deps := v02Deps(st)
	ctx := context.Background()
	createUserRow(t, st, "u1", "user")
	insertPlaybackEventRow(t, ctx, st, "u1", "ok", 111)

	admin := auth.UserContext{UserID: "admin", Role: "admin", Scopes: []string{"admin"}}
	rec := doReqAs(t, http.MethodGet, "/api/playback/events?user_id=u1", "", admin, func(r chi.Router) {
		deps.MountV02Management(r)
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"echo_user_id":"u1"`) {
		t.Fatalf("status=%d body=%s, want admin read of u1 events", rec.Code, rec.Body.String())
	}
}

// TestListPlaybackSessionsRedactsTokenCredentials is the regression guard for the
// credential-leak fix: GET /api/playback/sessions must never echo the live token
// selector or token_hash, whether read by an admin (admin-wide query) or by the
// owning user (user-scoped query).
func TestListPlaybackSessionsRedactsTokenCredentials(t *testing.T) {
	st := newAPIStore(t)
	deps := v02Deps(st)
	ctx := context.Background()
	createUserRow(t, st, "u1", "user")
	insertPlaybackSessionRow(t, ctx, st, "sess-1", "u1", "sel-leak", "hash-leak")

	reg := func(r chi.Router) { deps.MountV02Management(r) }
	cases := []struct {
		name string
		user auth.UserContext
	}{
		{"admin", auth.UserContext{UserID: "admin", Role: "admin", Scopes: []string{"admin"}}},
		{"owner", auth.UserContext{UserID: "u1", Role: "user", Scopes: []string{"read"}}},
	}
	for _, tc := range cases {
		rec := doReqAs(t, http.MethodGet, "/api/playback/sessions", "", tc.user, reg)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s, want 200", tc.name, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"id":"sess-1"`) {
			t.Fatalf("%s: session row missing, body=%s", tc.name, body)
		}
		if strings.Contains(body, "sel-leak") {
			t.Fatalf("%s: response leaked selector value, body=%s", tc.name, body)
		}
		if strings.Contains(body, "hash-leak") {
			t.Fatalf("%s: response leaked token hash value, body=%s", tc.name, body)
		}
		if strings.Contains(body, "token_hash") {
			t.Fatalf("%s: response carries token_hash key, body=%s", tc.name, body)
		}
		if strings.Contains(body, "selector") {
			t.Fatalf("%s: response carries selector key, body=%s", tc.name, body)
		}
	}
}

// TestListPlaybackSessionsCrossUserDenied verifies the owner-scoping carries over
// to the sessions endpoint: a non-admin asking for another user's sessions is 403,
// while an admin filtering by ?user_id= is scoped to that user.
func TestListPlaybackSessionsCrossUserDenied(t *testing.T) {
	st := newAPIStore(t)
	deps := v02Deps(st)
	ctx := context.Background()
	createUserRow(t, st, "u1", "user")
	createUserRow(t, st, "u2", "user")
	insertPlaybackSessionRow(t, ctx, st, "sess-u1", "u1", "sel-u1", "hash-u1")
	insertPlaybackSessionRow(t, ctx, st, "sess-u2", "u2", "sel-u2", "hash-u2")
	reg := func(r chi.Router) { deps.MountV02Management(r) }

	user := auth.UserContext{UserID: "u1", Role: "user", Scopes: []string{"read"}}
	rec := doReqAs(t, http.MethodGet, "/api/playback/sessions?user_id=u2", "", user, reg)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-user sessions status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}

	admin := auth.UserContext{UserID: "admin", Role: "admin", Scopes: []string{"admin"}}
	rec = doReqAs(t, http.MethodGet, "/api/playback/sessions?user_id=u1", "", admin, reg)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin scoped sessions status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"sess-u1"`) || strings.Contains(body, `"id":"sess-u2"`) {
		t.Fatalf("admin ?user_id=u1 not scoped to u1, body=%s", body)
	}
}

// TestGetQuotaUsageUnknownUser covers the 404 path: an admin querying usage for a
// user that does not exist must get 404, not a generic 500.
func TestGetQuotaUsageUnknownUser(t *testing.T) {
	st := newAPIStore(t)
	deps := v02Deps(st)
	admin := auth.UserContext{UserID: "admin", Role: "admin", Scopes: []string{"admin"}}
	rec := doReqAs(t, http.MethodGet, "/api/quota/usage?user_id=nonexistent", "", admin, func(r chi.Router) {
		deps.MountV02Management(r)
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown-user quota status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
}
