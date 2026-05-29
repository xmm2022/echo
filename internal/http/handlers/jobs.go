package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/job"
	"github.com/xmm2022/echo/internal/store/queries"
)

type jobResponse struct {
	ID         int64         `json:"id"`
	Kind       string        `json:"kind"`
	Status     string        `json:"status"`
	Progress   *job.Progress `json:"progress,omitempty"`
	Error      string        `json:"error,omitempty"`
	CreatedAt  int64         `json:"created_at"`
	StartedAt  *int64        `json:"started_at,omitempty"`
	FinishedAt *int64        `json:"finished_at,omitempty"`
}

type jobDetailResponse struct {
	jobResponse
	Payload      json.RawMessage       `json:"payload"`
	ProducerRuns []producerRunResponse `json:"producer_runs"`
}

type producerRunResponse struct {
	ID         int64  `json:"id"`
	Tool       string `json:"tool"`
	Cmdline    string `json:"cmdline"`
	ExitCode   *int64 `json:"exit_code,omitempty"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt *int64 `json:"finished_at,omitempty"`
}

// toJobResponse maps a job row to its API shape. A malformed progress column is
// reported via the error so the caller can log it; the response is still usable
// (progress stays nil) since progress is a best-effort display field.
func toJobResponse(j queries.Job) (jobResponse, error) {
	resp := jobResponse{ID: j.ID, Kind: j.Kind, Status: j.Status, CreatedAt: j.CreatedAt}
	if j.Error.Valid {
		resp.Error = j.Error.String
	}
	resp.StartedAt = nullableInt(j.StartedAt)
	resp.FinishedAt = nullableInt(j.FinishedAt)
	prog, ok, err := job.ParseProgress(j.Progress)
	if err != nil {
		return resp, err
	}
	if ok {
		resp.Progress = &prog
	}
	return resp, nil
}

// ListJobs serves GET /api/jobs[?status=&limit=]. Without a status filter it lists
// the most recent jobs newest-first.
func (d APIDeps) ListJobs(w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r, 50, 500)
	status := strings.TrimSpace(r.URL.Query().Get("status"))

	var jobs []queries.Job
	var err error
	if status != "" {
		jobs, err = d.Store.ListJobsByStatus(r.Context(), queries.ListJobsByStatusParams{Status: status, Limit: limit})
	} else {
		jobs, err = d.Store.ListJobs(r.Context(), queries.ListJobsParams{Limit: limit, Offset: 0})
	}
	if err != nil {
		d.logger().Error("jobs: list", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}

	out := make([]jobResponse, 0, len(jobs))
	for _, j := range jobs {
		resp, perr := toJobResponse(j)
		if perr != nil {
			d.logger().Warn("jobs: parse progress", "job_id", j.ID, "err", perr)
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetJob serves GET /api/jobs/{id} — the job plus its parsed progress and any
// producer runs.
func (d APIDeps) GetJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parseInt64Param(w, r, "id")
	if !ok {
		return
	}
	j, err := d.Store.GetJob(r.Context(), queries.GetJobParams{ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		d.logger().Error("jobs: get", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	resp, perr := toJobResponse(j)
	if perr != nil {
		d.logger().Warn("jobs: parse progress", "job_id", j.ID, "err", perr)
	}

	runs, err := d.Store.ListProducerRunsByJob(r.Context(), queries.ListProducerRunsByJobParams{JobID: id})
	if err != nil {
		d.logger().Error("jobs: list producer runs", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}

	writeJSON(w, http.StatusOK, jobDetailResponse{
		jobResponse:  resp,
		Payload:      redactJobPayload(j.Payload),
		ProducerRuns: toProducerRuns(runs),
	})
}

// redactJobPayload masks credential-bearing producer args (share_url query
// secrets, receive_code, secret file refs) before the payload is exposed in the
// job detail. Manual-ingest payloads carry only paths and pass through unchanged.
// A payload that does not decode as an IngestPayload is withheld rather than
// risk leaking an unknown shape.
func redactJobPayload(raw string) json.RawMessage {
	var p job.IngestPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return json.RawMessage(`{}`)
	}
	if p.Args != nil {
		p.Args = ingest.RedactArgs(p.Args)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(body)
}

// CancelJob serves POST /api/jobs/{id}/cancel. It asks the runner to cancel the
// job; canceled=false means the job is not currently running under this runner
// (already finished, pending, or unknown to the runner). A non-existent job id is
// 404.
func (d APIDeps) CancelJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parseInt64Param(w, r, "id")
	if !ok {
		return
	}
	if _, err := d.Store.GetJob(r.Context(), queries.GetJobParams{ID: id}); errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "job not found")
		return
	} else if err != nil {
		d.logger().Error("jobs: lookup for cancel", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	canceled := d.Jobs.Cancel(id)
	writeJSON(w, http.StatusOK, map[string]bool{"canceled": canceled})
}

func toProducerRuns(runs []queries.ProducerRun) []producerRunResponse {
	out := make([]producerRunResponse, 0, len(runs))
	for _, run := range runs {
		out = append(out, producerRunResponse{
			ID:         run.ID,
			Tool:       run.Tool,
			Cmdline:    run.Cmdline,
			ExitCode:   nullableInt(run.ExitCode),
			StartedAt:  run.StartedAt,
			FinishedAt: nullableInt(run.FinishedAt),
		})
	}
	return out
}

func nullableInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
