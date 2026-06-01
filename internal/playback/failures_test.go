package playback

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store/queries"
)

func copyIDParam(id int64) queries.ListCopyFailuresByCopyParams {
	return queries.ListCopyFailuresByCopyParams{CopyID: sql.NullInt64{Int64: id, Valid: true}}
}

func TestApplyCopyFailureWritesConfirmedAndSuspectStates(t *testing.T) {
	t.Run("confirmed object-missing on link marks copy dead", func(t *testing.T) {
		st := newPlaybackTestStore(t)
		ctx := context.Background()
		_, entry, account := createPlaybackUserEntryAndPool(t, ctx, st, time.Unix(1000, 0))
		copy := createPlaybackCopy(t, ctx, st, entry.BlobID, account.ID, "115", 10)
		recorder := NewFailureRecorder(st.Queries, nowFunc(time.Unix(1000, 0)))

		typed := &sidecarclient.SidecarTypedError{
			Kind: sidecarclient.SidecarErrObjectMissing, Operation: "link", HTTPStatus: http.StatusOK,
			OpenListCode: 500, SafeMessage: "object not found", EvidenceClass: "json_envelope", Confidence: "confirmed",
		}
		if err := recorder.ApplyCopyFailure(ctx, copy.ID, account.ID, typed, "req1"); err != nil {
			t.Fatal(err)
		}

		got, err := st.GetFileCopyByID(ctx, queries.GetFileCopyByIDParams{ID: copy.ID})
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "dead" || got.SchedulerState != "confirmed_dead" {
			t.Fatalf("copy state = %s/%s, want dead/confirmed_dead", got.Status, got.SchedulerState)
		}
		if got.LastFailureConfidence.String != "confirmed" {
			t.Fatalf("last_failure_confidence = %q, want confirmed", got.LastFailureConfidence.String)
		}

		failures, err := st.ListCopyFailuresByCopy(ctx, copyIDParam(copy.ID))
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 1 {
			t.Fatalf("copy_failures rows = %d, want 1", len(failures))
		}
		f := failures[0]
		if f.Confidence != "confirmed" || f.EvidenceClass != "json_envelope" {
			t.Fatalf("failure row confidence/evidence = %s/%s, want confirmed/json_envelope", f.Confidence, f.EvidenceClass)
		}
		if f.SidecarID != account.SidecarID || f.StorageMount != account.StorageMount {
			t.Fatalf("failure row sidecar/mount = %s/%s, want %s/%s", f.SidecarID, f.StorageMount, account.SidecarID, account.StorageMount)
		}
		if f.SafeMessage.String != "object not found" {
			t.Fatalf("failure row safe_message = %q, want %q", f.SafeMessage.String, "object not found")
		}
	})

	t.Run("non-link object-missing stays live but suspect", func(t *testing.T) {
		st := newPlaybackTestStore(t)
		ctx := context.Background()
		_, entry, account := createPlaybackUserEntryAndPool(t, ctx, st, time.Unix(2000, 0))
		copy := createPlaybackCopy(t, ctx, st, entry.BlobID, account.ID, "115", 20)
		recorder := NewFailureRecorder(st.Queries, nowFunc(time.Unix(2000, 0)))

		// object_missing but Operation="stream" fails the confirmed predicate, so this
		// must be recorded as suspect: scheduler_state flips but status stays 'live'.
		typed := &sidecarclient.SidecarTypedError{
			Kind: sidecarclient.SidecarErrObjectMissing, Operation: "stream", HTTPStatus: http.StatusOK,
			OpenListCode: 500, SafeMessage: "object not found", EvidenceClass: "json_envelope", Confidence: "confirmed",
		}
		if err := recorder.ApplyCopyFailure(ctx, copy.ID, account.ID, typed, "req2"); err != nil {
			t.Fatal(err)
		}

		got, err := st.GetFileCopyByID(ctx, queries.GetFileCopyByIDParams{ID: copy.ID})
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "live" {
			t.Fatalf("copy status = %s, want live (suspect path must not kill the copy)", got.Status)
		}
		if got.SchedulerState != "suspect_dead" {
			t.Fatalf("copy scheduler_state = %s, want suspect_dead", got.SchedulerState)
		}
		if got.LastFailureConfidence.String != "suspect" {
			t.Fatalf("last_failure_confidence = %q, want suspect", got.LastFailureConfidence.String)
		}
		if !got.VerifyAfter.Valid || got.VerifyAfter.Int64 != time.Unix(2000, 0).Add(suspectRetryDelay).Unix() {
			t.Fatalf("verify_after = %+v, want %d", got.VerifyAfter, time.Unix(2000, 0).Add(suspectRetryDelay).Unix())
		}

		failures, err := st.ListCopyFailuresByCopy(ctx, copyIDParam(copy.ID))
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 1 {
			t.Fatalf("copy_failures rows = %d, want 1", len(failures))
		}
		if failures[0].Confidence != "confirmed" {
			// The classifier stamped Confidence="confirmed" on the typed error; the
			// suspect *classification* comes from the predicate, but the failure row
			// faithfully records the typed error's own confidence field.
			t.Fatalf("failure row confidence = %q, want confirmed (verbatim from typed error)", failures[0].Confidence)
		}
	})

	t.Run("missing copy returns error", func(t *testing.T) {
		st := newPlaybackTestStore(t)
		ctx := context.Background()
		recorder := NewFailureRecorder(st.Queries, nowFunc(time.Unix(3000, 0)))
		typed := &sidecarclient.SidecarTypedError{
			Kind: sidecarclient.SidecarErrObjectMissing, Operation: "link", HTTPStatus: http.StatusOK,
			OpenListCode: 500, SafeMessage: "object not found", EvidenceClass: "json_envelope", Confidence: "confirmed",
		}
		if err := recorder.ApplyCopyFailure(ctx, 999999, "acct1", typed, "req3"); err == nil {
			t.Fatal("ApplyCopyFailure on missing copy = nil, want error")
		}
	})
}
