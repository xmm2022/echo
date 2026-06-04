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

func TestProjectUserMediaSubscriptionStatusListsLatestSafeMatch(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	q := st.Queries

	seed := seedDiscoveryDispatchTest(t, ctx, q)
	subscription, err := q.GetDiscoverySubscription(ctx, queries.GetDiscoverySubscriptionParams{ID: seed.SubscriptionID})
	if err != nil {
		t.Fatalf("get seeded subscription: %v", err)
	}
	secondSubscription, err := q.CreateDiscoverySubscription(ctx, queries.CreateDiscoverySubscriptionParams{
		OwnerID:           "admin",
		TmdbID:            "456",
		MediaType:         "movie",
		TmdbLanguage:      "zh-CN",
		TitleSnapshot:     "Second Movie",
		LibraryID:         subscription.LibraryID,
		ProducerProfileID: subscription.ProducerProfileID,
		RuleProfileID:     subscription.RuleProfileID,
		Status:            "active",
		SeasonFilterJson:  sql.NullString{},
		NextCheckAt:       sql.NullInt64{},
		CreatedAt:         2,
		UpdatedAt:         2,
	})
	if err != nil {
		t.Fatalf("create second subscription: %v", err)
	}

	firstUserSub, err := q.UpsertUserMediaSubscription(ctx, queries.UpsertUserMediaSubscriptionParams{
		EchoUserID:              "admin",
		RequestID:               sql.NullInt64{},
		DiscoverySubscriptionID: subscription.ID,
		TmdbID:                  subscription.TmdbID,
		MediaType:               subscription.MediaType,
		SeasonFilterJson:        sql.NullString{},
		SeasonFilterKey:         "all",
		Status:                  "active",
		CreatedAt:               10,
		UpdatedAt:               30,
	})
	if err != nil {
		t.Fatalf("upsert first user media subscription: %v", err)
	}
	secondUserSub, err := q.UpsertUserMediaSubscription(ctx, queries.UpsertUserMediaSubscriptionParams{
		EchoUserID:              "admin",
		RequestID:               sql.NullInt64{},
		DiscoverySubscriptionID: secondSubscription.ID,
		TmdbID:                  secondSubscription.TmdbID,
		MediaType:               secondSubscription.MediaType,
		SeasonFilterJson:        sql.NullString{},
		SeasonFilterKey:         "all",
		Status:                  "active",
		CreatedAt:               20,
		UpdatedAt:               20,
	})
	if err != nil {
		t.Fatalf("upsert second user media subscription: %v", err)
	}

	createProjectionSubscriptionMatch(t, ctx, q, seed.SourceID, subscription.ID, seed.RuleProfileID, "older", 10)
	newer := createProjectionSubscriptionMatch(t, ctx, q, seed.SourceID, subscription.ID, seed.RuleProfileID, "newer", 40)

	blob := createBlob(t, ctx, q, 4096)
	entry, err := q.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   subscription.LibraryID,
		RelPath:     "projection/movie.mkv",
		Name:        "movie.mkv",
		BlobID:      blob.ID,
		EchoWritten: 1,
		CreatedAt:   41,
		UpdatedAt:   41,
	})
	if err != nil {
		t.Fatalf("create result library entry: %v", err)
	}
	copyRow, err := q.InsertFileCopy(ctx, queries.InsertFileCopyParams{
		BlobID:       blob.ID,
		Provider:     "115",
		AccountID:    "dispatch-115",
		SidecarID:    "default",
		StorageMount: "/dispatch-115",
		RemotePath:   "/projection/movie.mkv",
		Status:       "live",
		LastSeen:     41,
	})
	if err != nil {
		t.Fatalf("create result file copy: %v", err)
	}
	if err := q.UpdateSubscriptionMatchResult(ctx, queries.UpdateSubscriptionMatchResultParams{
		Decision:             "imported",
		DispatchState:        "succeeded",
		ResultLibraryEntryID: sql.NullInt64{Int64: entry.ID, Valid: true},
		ResultBlobID:         sql.NullInt64{Int64: blob.ID, Valid: true},
		ResultCopyID:         sql.NullInt64{Int64: copyRow.ID, Valid: true},
		FailureKind:          sql.NullString{},
		FailureMessage:       sql.NullString{},
		UpdatedAt:            50,
		FinishedAt:           sql.NullInt64{Int64: 45, Valid: true},
		ID:                   newer.ID,
	}); err != nil {
		t.Fatalf("mark latest match imported: %v", err)
	}

	rows, err := q.ProjectUserMediaSubscriptionStatus(ctx, queries.ProjectUserMediaSubscriptionStatusParams{
		EchoUserID: "admin",
		Limit:      10,
		Offset:     0,
	})
	if err != nil {
		t.Fatalf("project user media subscription status: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0].UserMediaSubscriptionID != firstUserSub.ID || rows[1].UserMediaSubscriptionID != secondUserSub.ID {
		t.Fatalf("rows order = [%d,%d], want updated_at desc [%d,%d]",
			rows[0].UserMediaSubscriptionID, rows[1].UserMediaSubscriptionID, firstUserSub.ID, secondUserSub.ID)
	}
	if !rows[0].MatchID.Valid || rows[0].MatchID.Int64 != newer.ID {
		t.Fatalf("latest match id = %+v, want %d", rows[0].MatchID, newer.ID)
	}
	if !rows[0].MatchDecision.Valid || rows[0].MatchDecision.String != "imported" {
		t.Fatalf("match decision = %+v, want imported", rows[0].MatchDecision)
	}
	if !rows[0].MatchDispatchState.Valid || rows[0].MatchDispatchState.String != "succeeded" {
		t.Fatalf("match dispatch state = %+v, want succeeded", rows[0].MatchDispatchState)
	}
	if !rows[0].MatchResultLibraryEntryID.Valid || rows[0].MatchResultLibraryEntryID.Int64 != entry.ID {
		t.Fatalf("match result entry id = %+v, want %d", rows[0].MatchResultLibraryEntryID, entry.ID)
	}
	if !rows[0].MatchResultBlobID.Valid || rows[0].MatchResultBlobID.Int64 != blob.ID {
		t.Fatalf("match result blob id = %+v, want %d", rows[0].MatchResultBlobID, blob.ID)
	}
	if !rows[0].MatchResultCopyID.Valid || rows[0].MatchResultCopyID.Int64 != copyRow.ID {
		t.Fatalf("match result copy id = %+v, want %d", rows[0].MatchResultCopyID, copyRow.ID)
	}
	if !rows[0].MatchFinishedAt.Valid || rows[0].MatchFinishedAt.Int64 != 45 {
		t.Fatalf("match finished_at = %+v, want 45", rows[0].MatchFinishedAt)
	}
	if !rows[0].MatchUpdatedAt.Valid || rows[0].MatchUpdatedAt.Int64 != 50 {
		t.Fatalf("match updated_at = %+v, want 50", rows[0].MatchUpdatedAt)
	}
	if rows[1].MatchID.Valid {
		t.Fatalf("second subscription match id = %+v, want NULL from left join", rows[1].MatchID)
	}
}

func createProjectionSubscriptionMatch(
	t *testing.T,
	ctx context.Context,
	q *queries.Queries,
	sourceID int64,
	subscriptionID int64,
	ruleProfileID int64,
	externalKey string,
	updatedAt int64,
) queries.SubscriptionMatch {
	t.Helper()

	resource, err := q.UpsertDiscoveredResource(ctx, queries.UpsertDiscoveredResourceParams{
		SourceID:         sourceID,
		Provider:         "115",
		LinkKind:         "115_share",
		ExternalKey:      "projection-" + externalKey,
		TmdbID:           sql.NullString{String: "123", Valid: true},
		MediaType:        sql.NullString{String: "movie", Valid: true},
		Title:            sql.NullString{String: "Movie", Valid: true},
		SeasonNumber:     sql.NullInt64{},
		EpisodeStart:     sql.NullInt64{},
		EpisodeEnd:       sql.NullInt64{},
		ShareCode:        sql.NullString{String: "secret-share", Valid: true},
		ReceiveCode:      sql.NullString{String: "secret-receive", Valid: true},
		ShareUrlRedacted: sql.NullString{String: "https://115.example/s/redacted", Valid: true},
		RawTextRedacted:  sql.NullString{String: "redacted", Valid: true},
		RawTextRef:       sql.NullString{String: "raw-ref-secret", Valid: true},
		ParsedJson:       "{}",
		FeatureJson:      "{}",
		Status:           "accepted",
		FirstSeenAt:      updatedAt,
		LastSeenAt:       updatedAt,
	})
	if err != nil {
		t.Fatalf("upsert projection resource %s: %v", externalKey, err)
	}
	match, err := q.CreateSubscriptionMatch(ctx, queries.CreateSubscriptionMatchParams{
		SubscriptionID:     subscriptionID,
		ResourceID:         resource.ID,
		RuleProfileID:      ruleProfileID,
		RuleProfileVersion: 1,
		SeasonNumber:       sql.NullInt64{},
		EpisodeStart:       sql.NullInt64{},
		EpisodeEnd:         sql.NullInt64{},
		ScoreJson:          "{}",
		PreviousScoreJson:  sql.NullString{},
		Decision:           "queue",
		Reason:             "projection-test",
		DispatchState:      "queued",
		IdempotencyKey:     "projection-match-" + externalKey,
		CreatedAt:          updatedAt,
		UpdatedAt:          updatedAt,
		DecidedAt:          sql.NullInt64{Int64: updatedAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("create projection match %s: %v", externalKey, err)
	}
	return match
}
