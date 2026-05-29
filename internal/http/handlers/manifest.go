package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/xmm2022/echo/internal/store/queries"
)

const (
	defaultEntryLimit int64 = 200
	maxEntryLimit     int64 = 2000
)

type entryResponse struct {
	ID          int64  `json:"id"`
	RelPath     string `json:"rel_path"`
	Name        string `json:"name"`
	BlobID      int64  `json:"blob_id"`
	EchoWritten bool   `json:"echo_written"`
	LiveCopies  int64  `json:"live_copies"`
	UpdatedAt   int64  `json:"updated_at"`
}

// ListLibraryEntries serves GET /api/libraries/{id}/entries[?prefix=&limit=]. It
// lists a library's .echo entries (rel_path, echo_written, live copy count). The
// optional prefix is a literal rel_path prefix — matched with a half-open range so
// that _ and % in file names are NOT treated as wildcards (spec §13: no path-based
// lookups; this is read-only listing within one library).
func (d APIDeps) ListLibraryEntries(w http.ResponseWriter, r *http.Request) {
	id, ok := parseInt64Param(w, r, "id")
	if !ok {
		return
	}
	if _, err := d.Store.GetLibrary(r.Context(), queries.GetLibraryParams{ID: id}); errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "library not found")
		return
	} else if err != nil {
		d.logger().Error("entries: load library", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}

	limit := queryLimit(r, defaultEntryLimit, maxEntryLimit)
	entries, err := d.queryLibraryEntries(r.Context(), id, r.URL.Query().Get("prefix"), limit)
	if err != nil {
		d.logger().Error("entries: list", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (d APIDeps) queryLibraryEntries(ctx context.Context, libraryID int64, prefix string, limit int64) ([]entryResponse, error) {
	hi, hasBound := prefixUpperBound(prefix)
	// Empty prefix (or the pathological all-0xFF prefix with no finite bound) lists
	// the whole library; a normal prefix uses the indexed half-open range.
	if prefix == "" || !hasBound {
		rows, err := d.Store.ListLibraryEntries(ctx, queries.ListLibraryEntriesParams{LibraryID: libraryID, Limit: limit})
		if err != nil {
			return nil, err
		}
		out := make([]entryResponse, 0, len(rows))
		for _, row := range rows {
			if prefix != "" && !strings.HasPrefix(row.RelPath, prefix) {
				continue
			}
			out = append(out, entryResponse{
				ID: row.ID, RelPath: row.RelPath, Name: row.Name, BlobID: row.BlobID,
				EchoWritten: row.EchoWritten == 1, LiveCopies: row.LiveCopies, UpdatedAt: row.UpdatedAt,
			})
		}
		return out, nil
	}

	rows, err := d.Store.ListLibraryEntriesByLibraryPrefix(ctx, queries.ListLibraryEntriesByLibraryPrefixParams{
		LibraryID: libraryID, PrefixLo: prefix, PrefixHi: hi, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]entryResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, entryResponse{
			ID: row.ID, RelPath: row.RelPath, Name: row.Name, BlobID: row.BlobID,
			EchoWritten: row.EchoWritten == 1, LiveCopies: row.LiveCopies, UpdatedAt: row.UpdatedAt,
		})
	}
	return out, nil
}

// prefixUpperBound returns the exclusive upper bound of a half-open prefix range:
// the smallest string greater than every string beginning with prefix. ok=false
// when no finite bound exists (empty prefix, or all bytes are 0xFF), meaning
// "match everything from the prefix onward".
func prefixUpperBound(prefix string) (string, bool) {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] != 0xFF {
			out := make([]byte, i+1)
			copy(out, b[:i+1])
			out[i]++
			return string(out), true
		}
	}
	return "", false
}
