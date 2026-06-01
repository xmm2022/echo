package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
)

// maxAPIBodyBytes bounds JSON request bodies. It matches the nginx
// client_max_body_size in the deployment example (spec §9 / R2): API payloads are
// small control messages, never file uploads.
const maxAPIBodyBytes = 1 << 20 // 1 MiB

// StorageLister is the sidecar subset the accounts handler needs to verify a
// storage exists before binding an account to it (spec §6). *sidecarclient.Client
// satisfies it.
type StorageLister interface {
	ListStorages(ctx context.Context) ([]sidecarclient.Storage, error)
}

// JobController is the job-runner subset the ingest and jobs handlers need.
// *job.Runner satisfies it; the interface keeps handlers decoupled from the
// runner implementation and easy to fake in tests.
type JobController interface {
	Enqueue(ctx context.Context, kind string, payload any) (int64, error)
	Cancel(jobID int64) bool
}

// APIConfig is the config subset the admin API handlers consult. It is built from
// config.Config in cmd/echo; handlers take only what they need so their tests do
// not depend on full config validation.
type APIConfig struct {
	// ManualImportRoots and ProducerWorkdirRoot are the roots a manual import's
	// cas_tree_path may live under (spec §3 / plan R2).
	ManualImportRoots   []string
	ProducerWorkdirRoot string
	// EchoOutputBasePath is the root a library's echo_output_path must resolve
	// under when a library is created.
	EchoOutputBasePath string
	// Producer drives the synchronous producer arg pre-flight in the producer
	// ingest handler.
	Producer ingest.ProducerConfig
}

// APIDeps wires the admin API handlers (accounts / libraries / ingest / jobs /
// manifest / conflicts). Restore and stream keep their own dep structs.
type APIDeps struct {
	Store               *store.Store
	Sidecar             StorageLister
	Jobs                JobController
	Config              APIConfig
	Logger              *slog.Logger
	Now                 func() time.Time
	BootstrapAdminToken string
}

// Mount registers every admin API route on r. Callers mount it inside the
// authenticated group so no route escapes auth (plan §10 acceptance: a scan of
// chi Group/Mount shows no API route outside the authed group).
func (d APIDeps) Mount(r chi.Router) {
	r.Get("/api/accounts", d.ListAccounts)
	r.Post("/api/accounts", d.CreateAccount)
	r.Delete("/api/accounts/{id}", d.DeleteAccount)

	r.Get("/api/libraries", d.ListLibraries)
	r.Post("/api/libraries", d.CreateLibrary)
	r.Delete("/api/libraries/{id}", d.DeleteLibrary)
	r.Get("/api/libraries/{id}/entries", d.ListLibraryEntries)

	r.Post("/api/ingest/manual", d.IngestManual)
	r.Post("/api/ingest/producer", d.IngestProducer)

	r.Get("/api/jobs", d.ListJobs)
	r.Get("/api/jobs/{id}", d.GetJob)
	r.Post("/api/jobs/{id}/cancel", d.CancelJob)

	r.Get("/api/conflicts", d.ListConflicts)
	r.Post("/api/conflicts/{id}/dismiss", d.DismissConflict)

	d.MountV02Management(r)
}

func (d APIDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d APIDeps) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// decodeJSON reads a bounded JSON body into dst, rejecting unknown fields. On any
// error it writes 400 and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func writeAPIError(w http.ResponseWriter, status int, reason string) {
	writeJSON(w, status, map[string]string{"error": reason})
}

// parseInt64Param parses a positive int64 chi URL param. On a malformed value it
// writes 400 and returns ok=false.
func parseInt64Param(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid "+name)
		return 0, false
	}
	return id, true
}

// queryLimit reads a positive ?limit= with a default and ceiling.
func queryLimit(r *http.Request, def, max int64) int64 {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// queryOffset reads a non-negative ?offset= (default 0).
func queryOffset(r *http.Request) int64 {
	n, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
