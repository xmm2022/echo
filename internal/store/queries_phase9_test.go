package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/xmm2022/echo/internal/store/queries"
)

func TestListJobsNewestFirst(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	older := createJobWith(t, ctx, st, "done", 10)
	newest := createJobWith(t, ctx, st, "failed", 30)
	middle := createJobWith(t, ctx, st, "pending", 20)

	jobs, err := st.ListJobs(ctx, queries.ListJobsParams{Limit: 100, Offset: 0})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("len(jobs) = %d, want 3", len(jobs))
	}
	wantOrder := []int64{newest.ID, middle.ID, older.ID}
	for i, want := range wantOrder {
		if jobs[i].ID != want {
			t.Fatalf("jobs[%d].ID = %d, want %d (created_at DESC)", i, jobs[i].ID, want)
		}
	}

	// LIMIT/OFFSET paginate.
	page, err := st.ListJobs(ctx, queries.ListJobsParams{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("list jobs page: %v", err)
	}
	if len(page) != 1 || page[0].ID != middle.ID {
		t.Fatalf("page = %+v, want [middle]", page)
	}
}

func TestListLibraryEntriesByLibraryPrefix(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	createAccountWith(t, ctx, st.Queries, "115-main", "115", "/115-main")
	library := createLibrary(t, ctx, st.Queries)
	blobWithCopies := createBlob(t, ctx, st.Queries, 1024)
	blobNoCopies := createBlob(t, ctx, st.Queries, 2048)

	entry := func(rel string, blobID int64, echoWritten int64) {
		t.Helper()
		if _, err := st.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
			LibraryID:   library.ID,
			RelPath:     rel,
			Name:        rel,
			BlobID:      blobID,
			EchoWritten: echoWritten,
			CreatedAt:   1,
			UpdatedAt:   1,
		}); err != nil {
			t.Fatalf("upsert entry %q: %v", rel, err)
		}
	}
	entry("season/ep1.mkv", blobWithCopies.ID, 1)
	entry("season/ep2.mkv", blobNoCopies.ID, 0)
	entry("extras/clip.mkv", blobWithCopies.ID, 1)

	copyRow := func(remotePath, status string, lastSeen int64) {
		t.Helper()
		if _, err := st.InsertFileCopy(ctx, queries.InsertFileCopyParams{
			BlobID:       blobWithCopies.ID,
			Provider:     "115",
			AccountID:    "115-main",
			SidecarID:    "default",
			StorageMount: "/115-main",
			RemotePath:   remotePath,
			Status:       status,
			LastSeen:     lastSeen,
		}); err != nil {
			t.Fatalf("insert copy %q: %v", remotePath, err)
		}
	}
	copyRow("/a.mkv", "live", 10)
	copyRow("/b.mkv", "live", 20)
	copyRow("/c.mkv", "dead", 30)

	rows, err := st.ListLibraryEntriesByLibraryPrefix(ctx, queries.ListLibraryEntriesByLibraryPrefixParams{
		LibraryID: library.ID,
		PrefixLo:  "season/",
		PrefixHi:  "season0", // '/' (0x2f) bumped to '0' (0x30): half-open upper bound
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("list entries prefix: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 (only season/ entries)", len(rows))
	}
	if rows[0].RelPath != "season/ep1.mkv" || rows[1].RelPath != "season/ep2.mkv" {
		t.Fatalf("rows order = %q,%q want season/ep1,season/ep2", rows[0].RelPath, rows[1].RelPath)
	}
	if rows[0].LiveCopies != 2 {
		t.Fatalf("ep1 live_copies = %d, want 2 (dead copy excluded)", rows[0].LiveCopies)
	}
	if rows[0].EchoWritten != 1 {
		t.Fatalf("ep1 echo_written = %d, want 1", rows[0].EchoWritten)
	}
	if rows[1].LiveCopies != 0 {
		t.Fatalf("ep2 live_copies = %d, want 0", rows[1].LiveCopies)
	}

	all, err := st.ListLibraryEntries(ctx, queries.ListLibraryEntriesParams{
		LibraryID: library.ID,
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("list all entries: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all) = %d, want 3", len(all))
	}
}

func TestGetHashConflict(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	blobA := createBlob(t, ctx, st.Queries, 1)
	blobB := createBlob(t, ctx, st.Queries, 2)
	inserted, err := st.InsertHashConflict(ctx, queries.InsertHashConflictParams{
		BlobIDA:    blobA.ID,
		BlobIDB:    blobB.ID,
		Reason:     "hash_multi_blob",
		Detail:     "{}",
		ObservedAt: 5,
		Status:     "open",
	})
	if err != nil {
		t.Fatalf("insert conflict: %v", err)
	}

	got, err := st.GetHashConflict(ctx, queries.GetHashConflictParams{ID: inserted.ID})
	if err != nil {
		t.Fatalf("get conflict: %v", err)
	}
	if got.ID != inserted.ID || got.Reason != "hash_multi_blob" {
		t.Fatalf("got = %+v, want id=%d reason=hash_multi_blob", got, inserted.ID)
	}

	if _, err := st.GetHashConflict(ctx, queries.GetHashConflictParams{ID: 999999}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get missing conflict err = %v, want sql.ErrNoRows", err)
	}
}
