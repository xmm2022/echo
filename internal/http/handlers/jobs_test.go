package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func jobsDeps(st *store.Store, jobs JobController) APIDeps {
	return APIDeps{Store: st, Jobs: jobs, Logger: apiLogger(), Now: apiClock()}
}

func makeJob(t *testing.T, st *store.Store, status string, createdAt int64, payload string) queries.Job {
	t.Helper()
	j, err := st.CreateJob(context.Background(), queries.CreateJobParams{
		Kind: "ingest_manual", Status: status, Payload: payload, OwnerID: "admin", CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return j
}

func TestListJobs(t *testing.T) {
	st := newAPIStore(t)
	deps := jobsDeps(st, &fakeJobs{})
	makeJob(t, st, "done", 10, "{}")
	makeJob(t, st, "pending", 30, "{}")
	makeJob(t, st, "running", 20, "{}")

	rec := doReq(t, http.MethodGet, "/api/jobs", "", func(r chi.Router) { r.Get("/api/jobs", deps.ListJobs) })
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got []struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	decodeBody(t, rec, &got)
	if len(got) != 3 {
		t.Fatalf("jobs = %d, want 3", len(got))
	}
	if got[0].Status != "pending" { // created_at=30 is newest
		t.Fatalf("first job status = %q, want pending (newest first)", got[0].Status)
	}
}

func TestListJobsByStatus(t *testing.T) {
	st := newAPIStore(t)
	deps := jobsDeps(st, &fakeJobs{})
	makeJob(t, st, "done", 10, "{}")
	makeJob(t, st, "pending", 20, "{}")
	makeJob(t, st, "pending", 30, "{}")

	rec := doReq(t, http.MethodGet, "/api/jobs?status=pending", "", func(r chi.Router) { r.Get("/api/jobs", deps.ListJobs) })
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []struct {
		Status string `json:"status"`
	}
	decodeBody(t, rec, &got)
	if len(got) != 2 {
		t.Fatalf("filtered jobs = %d, want 2 pending", len(got))
	}
}

func TestGetJobDetail(t *testing.T) {
	ctx := context.Background()
	st := newAPIStore(t)
	deps := jobsDeps(st, &fakeJobs{})
	j := makeJob(t, st, "running", 10, `{"library_id":1}`)
	if err := st.UpdateJobProgress(ctx, queries.UpdateJobProgressParams{
		ID: j.ID, Progress: sql.NullString{String: `{"current":2,"total":5}`, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProducerRun(ctx, queries.CreateProducerRunParams{
		JobID: j.ID, Tool: "115share2cas", Cmdline: `["115share2cas","--share-url","x"]`,
		Workdir: "/w", OutputDir: "/w/cas", StartedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, http.MethodGet, "/api/jobs/"+itoa(j.ID), "", func(r chi.Router) { r.Get("/api/jobs/{id}", deps.GetJob) })
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID       int64 `json:"id"`
		Progress *struct {
			Current int `json:"current"`
			Total   int `json:"total"`
		} `json:"progress"`
		ProducerRuns []struct {
			Tool string `json:"tool"`
		} `json:"producer_runs"`
	}
	decodeBody(t, rec, &got)
	if got.Progress == nil || got.Progress.Current != 2 || got.Progress.Total != 5 {
		t.Fatalf("progress = %+v, want current=2 total=5", got.Progress)
	}
	if len(got.ProducerRuns) != 1 || got.ProducerRuns[0].Tool != "115share2cas" {
		t.Fatalf("producer_runs = %+v, want one 115share2cas", got.ProducerRuns)
	}
}

func TestGetJobRedactsProducerSecretsInPayload(t *testing.T) {
	st := newAPIStore(t)
	deps := jobsDeps(st, &fakeJobs{})
	// A producer payload whose args carry a share-url password + receive_code.
	payload := `{"library_id":1,"target_account":"a","tool":"115share2cas","args":{"share_url":"https://115.com/s/x?password=topsecret","receive_code":"zzzz","cookie_file":"ref:cookies/115.txt"}}`
	j := makeJob(t, st, "running", 10, payload)

	rec := doReq(t, http.MethodGet, "/api/jobs/"+itoa(j.ID), "", func(r chi.Router) { r.Get("/api/jobs/{id}", deps.GetJob) })
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leak := range []string{"topsecret", "zzzz", "cookies/115.txt"} {
		if strings.Contains(body, leak) {
			t.Fatalf("job detail payload leaks %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, "115share2cas") {
		t.Fatalf("expected tool name to remain in payload: %s", body)
	}
}

func TestGetJobNotFoundAndBadID(t *testing.T) {
	st := newAPIStore(t)
	deps := jobsDeps(st, &fakeJobs{})
	reg := func(r chi.Router) { r.Get("/api/jobs/{id}", deps.GetJob) }
	if rec := doReq(t, http.MethodGet, "/api/jobs/99999", "", reg); rec.Code != http.StatusNotFound {
		t.Fatalf("missing job = %d, want 404", rec.Code)
	}
	if rec := doReq(t, http.MethodGet, "/api/jobs/abc", "", reg); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id = %d, want 400", rec.Code)
	}
}

func TestCancelJob(t *testing.T) {
	st := newAPIStore(t)
	j := makeJob(t, st, "running", 10, "{}")

	running := &fakeJobs{cancelOK: true}
	deps := jobsDeps(st, running)
	rec := doReq(t, http.MethodPost, "/api/jobs/"+itoa(j.ID)+"/cancel", "", func(r chi.Router) {
		r.Post("/api/jobs/{id}/cancel", deps.CancelJob)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Canceled bool `json:"canceled"`
	}
	decodeBody(t, rec, &got)
	if !got.Canceled {
		t.Fatal("canceled = false, want true")
	}
	if len(running.canceled) != 1 || running.canceled[0] != j.ID {
		t.Fatalf("cancel called with %v, want [%d]", running.canceled, j.ID)
	}

	// Job exists but is not running under the runner -> canceled:false, still 200.
	notRunning := &fakeJobs{cancelOK: false}
	deps2 := jobsDeps(st, notRunning)
	rec = doReq(t, http.MethodPost, "/api/jobs/"+itoa(j.ID)+"/cancel", "", func(r chi.Router) {
		r.Post("/api/jobs/{id}/cancel", deps2.CancelJob)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	decodeBody(t, rec, &got)
	if got.Canceled {
		t.Fatal("canceled = true, want false for non-running job")
	}
}

func TestCancelJobNotFound(t *testing.T) {
	st := newAPIStore(t)
	deps := jobsDeps(st, &fakeJobs{cancelOK: true})
	rec := doReq(t, http.MethodPost, "/api/jobs/99999/cancel", "", func(r chi.Router) {
		r.Post("/api/jobs/{id}/cancel", deps.CancelJob)
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
