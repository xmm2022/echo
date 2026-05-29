package restore

import (
	"context"
	"time"

	"github.com/xmm2022/echo/internal/store/queries"
)

// maxLiveCopies bounds the candidate copies a single restore/stream request
// tries before giving up (spec §4 restore: ORDER BY ... LIMIT 5).
const maxLiveCopies = 5

// Querier is the subset of store queries the resolver depends on.
type Querier interface {
	GetLibraryEntryByID(ctx context.Context, arg queries.GetLibraryEntryByIDParams) (queries.LibraryEntry, error)
	ListLiveCopiesByBlob(ctx context.Context, arg queries.ListLiveCopiesByBlobParams) ([]queries.FileCopy, error)
	ListLiveCopiesByBlobPreferProvider(ctx context.Context, arg queries.ListLiveCopiesByBlobPreferProviderParams) ([]queries.FileCopy, error)
	MarkFileCopyDead(ctx context.Context, arg queries.MarkFileCopyDeadParams) error
}

// Resolver maps a file_id to its live copies and records copies the sidecar
// reports as gone.
type Resolver struct {
	q   Querier
	now func() time.Time
}

// NewResolver builds a Resolver. A nil now defaults to time.Now.
func NewResolver(q Querier, now func() time.Time) *Resolver {
	if now == nil {
		now = time.Now
	}
	return &Resolver{q: q, now: now}
}

// LiveCopies resolves file_id -> blob_id -> live copies. With an empty prefer the
// copies are ordered by last_seen DESC; otherwise the preferred provider sorts
// first (spec §4). A missing file_id surfaces as sql.ErrNoRows.
func (r *Resolver) LiveCopies(ctx context.Context, fileID int64, prefer string) ([]queries.FileCopy, error) {
	entry, err := r.q.GetLibraryEntryByID(ctx, queries.GetLibraryEntryByIDParams{ID: fileID})
	if err != nil {
		return nil, err
	}
	if prefer == "" {
		return r.q.ListLiveCopiesByBlob(ctx, queries.ListLiveCopiesByBlobParams{
			BlobID: entry.BlobID,
			Limit:  maxLiveCopies,
		})
	}
	return r.q.ListLiveCopiesByBlobPreferProvider(ctx, queries.ListLiveCopiesByBlobPreferProviderParams{
		PreferredProvider: prefer,
		BlobID:            entry.BlobID,
		Limit:             maxLiveCopies,
	})
}

// MarkDead flags a copy as dead after the sidecar reported it gone (404/410).
func (r *Resolver) MarkDead(ctx context.Context, copyID int64) error {
	return r.q.MarkFileCopyDead(ctx, queries.MarkFileCopyDeadParams{
		LastSeen: r.now().Unix(),
		ID:       copyID,
	})
}
