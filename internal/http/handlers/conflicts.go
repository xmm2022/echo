package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/xmm2022/echo/internal/store/queries"
)

// statusDismissed is the hash_conflicts.status an admin sets to clear a conflict
// from the open list (the column is free-form TEXT; ListOpenHashConflicts filters
// status='open').
const statusDismissed = "dismissed"

type conflictResponse struct {
	ID         int64           `json:"id"`
	BlobIDA    int64           `json:"blob_id_a"`
	BlobIDB    int64           `json:"blob_id_b"`
	Reason     string          `json:"reason"`
	Detail     json.RawMessage `json:"detail"`
	ObservedAt int64           `json:"observed_at"`
	Status     string          `json:"status"`
}

func toConflictResponse(c queries.HashConflict) conflictResponse {
	detail := json.RawMessage(c.Detail)
	if len(detail) == 0 {
		detail = json.RawMessage("{}")
	}
	return conflictResponse{
		ID:         c.ID,
		BlobIDA:    c.BlobIDA,
		BlobIDB:    c.BlobIDB,
		Reason:     c.Reason,
		Detail:     detail,
		ObservedAt: c.ObservedAt,
		Status:     c.Status,
	}
}

// ListConflicts serves GET /api/conflicts[?limit=&offset=] — the open hash
// conflicts, newest-observed first (spec §8 / data model).
func (d APIDeps) ListConflicts(w http.ResponseWriter, r *http.Request) {
	conflicts, err := d.Store.ListOpenHashConflicts(r.Context(), queries.ListOpenHashConflictsParams{
		Limit:  queryLimit(r, 100, 1000),
		Offset: queryOffset(r),
	})
	if err != nil {
		d.logger().Error("conflicts: list", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	out := make([]conflictResponse, 0, len(conflicts))
	for _, c := range conflicts {
		out = append(out, toConflictResponse(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// DismissConflict serves POST /api/conflicts/{id}/dismiss — marks the conflict
// dismissed so it leaves the open list. v0.1 records the admin decision; it does
// not auto-merge blobs (spec §3 / §4).
func (d APIDeps) DismissConflict(w http.ResponseWriter, r *http.Request) {
	id, ok := parseInt64Param(w, r, "id")
	if !ok {
		return
	}
	if _, err := d.Store.GetHashConflict(r.Context(), queries.GetHashConflictParams{ID: id}); errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "conflict not found")
		return
	} else if err != nil {
		d.logger().Error("conflicts: lookup for dismiss", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	if err := d.Store.UpdateHashConflictStatus(r.Context(), queries.UpdateHashConflictStatusParams{
		Status: statusDismissed,
		ID:     id,
	}); err != nil {
		d.logger().Error("conflicts: dismiss", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": statusDismissed})
}
