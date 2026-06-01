package embyproxy

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/playback"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func discardStreamLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStreamSidecar satisfies the local Sidecar interface. It counts Stream calls
// so the HEAD test can assert the byte path is never taken, and returns an error
// when failOnStream is set so a GET path that DID call it would surface failure.
type fakeStreamSidecar struct {
	streamCalls  int
	failOnStream bool
	result       *sidecarclient.StreamResult
}

func (f *fakeStreamSidecar) Stream(ctx context.Context, req sidecarclient.StreamRequest) (*sidecarclient.StreamResult, error) {
	f.streamCalls++
	if f.failOnStream {
		return nil, &sidecarclient.SidecarTypedError{
			Kind:          sidecarclient.SidecarErrTransient,
			Operation:     "stream",
			HTTPStatus:    http.StatusInternalServerError,
			EvidenceClass: "html_snippet",
			Confidence:    "suspect",
		}
	}
	return f.result, nil
}

// streamHandlerTestDeps bundles a mounted handler with the inputs a test needs to
// drive it: a freshly minted, valid playback token and the fake sidecar to inspect.
type streamHandlerTestDeps struct {
	handler    http.Handler
	validToken string
	sidecar    *fakeStreamSidecar
	st         *store.Store
}

// newStreamHandlerTestDeps seeds a real store with everything a live playback
// session resolves to (emby server + user link, owner-owned mapped library entry,
// an account-backed pool, and a live file copy over the entry's blob), mints a
// playback session, and mounts a StreamHandler over real playback collaborators.
func newStreamHandlerTestDeps(t *testing.T) streamHandlerTestDeps {
	t.Helper()
	st := newEmbyProxyTestStore(t)
	ctx := context.Background()
	now := time.Unix(1000, 0)

	seedEmbyServerAndUserLink(t, ctx, st)
	entry := seedMappedLibraryEntry(t, ctx, st, "admin")
	account := seedStreamAccount(t, ctx, st, "acct1", "115")
	seedStreamPoolAssignment(t, ctx, st, "admin", account.ID, "115")
	seedLiveFileCopy(t, ctx, st, entry.BlobID, account.ID, "115", now.Unix())

	mgr := NewSessionManager(st.Queries, SessionConfig{TTL: time.Hour}, nowFunc(now))
	token, _, err := mgr.CreatePlaybackSession(ctx, CreatePlaybackSessionInput{
		EchoUserID: "admin", EmbyServerID: "default", EmbyUserID: "emby-u1",
		DeviceID: "dev1", ItemID: "item1", MediaSourceID: "ms1",
		LibraryEntryID: entry.ID, BlobID: entry.BlobID,
	})
	if err != nil {
		t.Fatalf("create playback session: %v", err)
	}

	sidecar := &fakeStreamSidecar{result: &sidecarclient.StreamResult{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"video/x-matroska"}},
		Body:       http.NoBody,
	}}
	resolver := playback.NewResolver(st.Queries, nowFunc(now))
	quota := playback.NewQuota(st.Queries, nowFunc(now), time.Hour)
	failures := playback.NewFailureRecorder(st.Queries, nowFunc(now))

	deps := &Deps{
		ProxyPrefix: "/emby",
		Stream:      StreamHandler(mgr, resolver, quota, sidecar, failures, discardStreamLogger()),
		Error:       ErrorHandler(mgr),
	}
	r := chi.NewRouter()
	deps.Mount(r)

	return streamHandlerTestDeps{handler: r, validToken: token, sidecar: sidecar, st: st}
}

func TestStreamHEADDoesNotCallSidecarStream(t *testing.T) {
	deps := newStreamHandlerTestDeps(t)
	deps.sidecar.failOnStream = true
	req := httptest.NewRequest(http.MethodHead, "/emby/stream/"+deps.validToken, nil)
	rec := httptest.NewRecorder()
	deps.handler.ServeHTTP(rec, req)
	if deps.sidecar.streamCalls != 0 {
		t.Fatalf("sidecar stream calls = %d, want 0 for HEAD", deps.sidecar.streamCalls)
	}
	if rec.Code >= 500 {
		t.Fatalf("HEAD status = %d, want controlled metadata response", rec.Code)
	}
}

func TestStreamGETInvalidTokenReturns404(t *testing.T) {
	deps := newStreamHandlerTestDeps(t)
	deps.sidecar.failOnStream = true // if the byte path were wrongly taken, it'd surface
	req := httptest.NewRequest(http.MethodGet, "/emby/stream/bogus.token", nil)
	rec := httptest.NewRecorder()
	deps.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invalid-token GET status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("X-Echo-Reason"); got != "temporary_unavailable" {
		t.Fatalf("X-Echo-Reason = %q, want temporary_unavailable", got)
	}
	if deps.sidecar.streamCalls != 0 {
		t.Fatalf("sidecar stream calls = %d, want 0 for invalid token", deps.sidecar.streamCalls)
	}
}

func TestStreamGETSuccessCountsBytesAndFinishesEvent(t *testing.T) {
	deps := newStreamHandlerTestDeps(t)
	const body = "hello-stream-bytes"
	deps.sidecar.result.Body = io.NopCloser(strings.NewReader(body))

	req := httptest.NewRequest(http.MethodGet, "/emby/stream/"+deps.validToken, nil)
	rec := httptest.NewRecorder()
	deps.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want no-referrer", got)
	}
	if rec.Body.String() != body {
		t.Fatalf("body = %q, want %q", rec.Body.String(), body)
	}
	if deps.sidecar.streamCalls != 1 {
		t.Fatalf("sidecar stream calls = %d, want 1", deps.sidecar.streamCalls)
	}

	// The finished playback_events row must reflect the counted bytes + ok status.
	var (
		status     string
		bytesSent  int64
		httpStatus sql.NullInt64
	)
	row := deps.st.DB.QueryRowContext(context.Background(),
		`SELECT status, bytes_sent, http_status FROM playback_events WHERE operation='stream' ORDER BY id DESC LIMIT 1`)
	if err := row.Scan(&status, &bytesSent, &httpStatus); err != nil {
		t.Fatalf("scan playback_events: %v", err)
	}
	if status != "ok" {
		t.Fatalf("event status = %q, want ok", status)
	}
	if bytesSent != int64(len(body)) {
		t.Fatalf("bytes_sent = %d, want %d", bytesSent, len(body))
	}
	if !httpStatus.Valid || httpStatus.Int64 != http.StatusOK {
		t.Fatalf("http_status = %v, want 200", httpStatus)
	}
}

// --- file_copies / account / pool seeding, cribbed from internal/playback/resolver_test.go ---

func seedStreamAccount(t *testing.T, ctx context.Context, st *store.Store, id, provider string) queries.Account {
	t.Helper()
	if err := st.CreateAccount(ctx, queries.CreateAccountParams{
		ID:           id,
		Provider:     provider,
		SidecarID:    "default",
		StorageMount: "/" + id,
		Status:       "ok",
		OwnerID:      "admin",
		CreatedAt:    1000,
		UpdatedAt:    1000,
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return queries.Account{ID: id, Provider: provider, SidecarID: "default", StorageMount: "/" + id, Status: "ok", OwnerID: "admin"}
}

func seedStreamPoolAssignment(t *testing.T, ctx context.Context, st *store.Store, echoUserID, accountID, provider string) {
	t.Helper()
	if _, err := st.CreateAccountPoolAssignment(ctx, queries.CreateAccountPoolAssignmentParams{
		EchoUserID:           echoUserID,
		Priority:             100,
		Weight:               1,
		MaxConcurrentStreams: sql.NullInt64{},
		DailyBytesLimit:      sql.NullInt64{},
		Enabled:              1,
		CreatedAt:            1000,
		UpdatedAt:            1000,
		AccountID:            accountID,
		Provider:             provider,
	}); err != nil {
		t.Fatalf("create pool assignment: %v", err)
	}
}

func seedLiveFileCopy(t *testing.T, ctx context.Context, st *store.Store, blobID int64, accountID, provider string, lastSeen int64) queries.FileCopy {
	t.Helper()
	copy, err := st.InsertFileCopy(ctx, queries.InsertFileCopyParams{
		BlobID:       blobID,
		Provider:     provider,
		AccountID:    accountID,
		SidecarID:    "default",
		StorageMount: "/" + accountID,
		RemotePath:   "/media/episode.mkv",
		CloudFileID:  sql.NullString{String: "cloud-1", Valid: true},
		Pickcode:     sql.NullString{String: "pick-1", Valid: true},
		Status:       "live",
		LastSeen:     lastSeen,
	})
	if err != nil {
		t.Fatalf("insert file copy: %v", err)
	}
	return copy
}
