package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func newWebStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("file:" + t.TempDir() + "/echo.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestIndexServesPublicShell(t *testing.T) {
	d := Deps{Store: newWebStore(t)}
	rec := httptest.NewRecorder()
	d.Index(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"Echo Admin", "/static/htmx.min.js", "/static/app.js", `id="admin-token"`, `hx-get="/ui/jobs"`, `hx-get="/ui/conflicts"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}
}

func TestStaticServesVendoredHtmx(t *testing.T) {
	rec := httptest.NewRecorder()
	Static().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "htmx") {
		t.Fatal("htmx.min.js body does not look like htmx")
	}
}

func TestUIJobsRendersTable(t *testing.T) {
	st := newWebStore(t)
	if _, err := st.CreateJob(context.Background(), queries.CreateJobParams{
		Kind: "ingest_manual", Status: "running", Payload: "{}", OwnerID: "admin", CreatedAt: 7,
	}); err != nil {
		t.Fatal(err)
	}
	d := Deps{Store: st}
	rec := httptest.NewRecorder()
	d.UIJobs(rec, httptest.NewRequest(http.MethodGet, "/ui/jobs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ingest_manual") || !strings.Contains(body, "running") {
		t.Fatalf("jobs fragment missing job data: %s", body)
	}
}

func TestUIConflictsRendersOpenOnly(t *testing.T) {
	ctx := context.Background()
	st := newWebStore(t)
	a, _ := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 1, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
	b, _ := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 2, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
	if _, err := st.InsertHashConflict(ctx, queries.InsertHashConflictParams{
		BlobIDA: a.ID, BlobIDB: b.ID, Reason: "hash_multi_blob", Detail: "{}", ObservedAt: 1, Status: "open",
	}); err != nil {
		t.Fatal(err)
	}
	d := Deps{Store: st}
	rec := httptest.NewRecorder()
	d.UIConflicts(rec, httptest.NewRequest(http.MethodGet, "/ui/conflicts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hash_multi_blob") {
		t.Fatalf("conflicts fragment missing conflict: %s", body)
	}
	if !strings.Contains(body, "/api/conflicts/") || !strings.Contains(body, `hx-swap="delete"`) {
		t.Fatalf("conflicts fragment missing dismiss button: %s", body)
	}
}

func TestMountUIRegistersRoutes(t *testing.T) {
	d := Deps{Store: newWebStore(t)}
	r := chi.NewRouter()
	d.MountUI(r)
	for _, path := range []string{"/ui/jobs", "/ui/conflicts"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200 (route mounted)", path, rec.Code)
		}
	}
}
