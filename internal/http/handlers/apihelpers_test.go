package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
)

// --- shared fakes for the admin API handler tests ---

type fakeStorageLister struct {
	storages []sidecarclient.Storage
	err      error
}

func (f fakeStorageLister) ListStorages(context.Context) ([]sidecarclient.Storage, error) {
	return f.storages, f.err
}

type enqueuedJob struct {
	kind    string
	payload any
}

type fakeJobs struct {
	enqueued []enqueuedJob
	nextID   int64
	enqErr   error
	cancelOK bool
	canceled []int64
}

func (f *fakeJobs) Enqueue(_ context.Context, kind string, payload any) (int64, error) {
	if f.enqErr != nil {
		return 0, f.enqErr
	}
	f.enqueued = append(f.enqueued, enqueuedJob{kind: kind, payload: payload})
	f.nextID++
	return f.nextID, nil
}

func (f *fakeJobs) Cancel(jobID int64) bool {
	f.canceled = append(f.canceled, jobID)
	return f.cancelOK
}

func apiLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newAPIStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "echo.db"))
	st, err := store.Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func apiClock() func() time.Time { return func() time.Time { return time.Unix(1000, 0) } }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// doReq mounts route patterns via register, then runs one request and returns the
// recorder. A non-empty body is sent as JSON.
func doReq(t *testing.T, method, target, body string, register func(chi.Router)) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	register(r)
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
}
