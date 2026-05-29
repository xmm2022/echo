package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmm2022/echo/internal/castree"
	"github.com/xmm2022/echo/internal/echofile"
	"github.com/xmm2022/echo/internal/pathsafe"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

const (
	defaultOwnerID          = "admin"
	defaultProgressInterval = time.Second
)

type Sidecar interface {
	PutCAS(context.Context, sidecarclient.PutCASRequest) (*sidecarclient.ItemResult, error)
}

type Job struct {
	ID            int64
	LibraryID     int64
	TargetAccount string
	TargetSubdir  string
	CASTreePath   string
	ManifestPath  string
	OwnerID       string
	Tool          string
	Args          map[string]any
}

type Config struct {
	WorkerPerJob     int
	ProgressInterval time.Duration
	Producer         ProducerConfig
}

type ProducerConfig struct {
	WorkdirRoot    string
	SecretsRoot    string
	DefaultTimeout time.Duration
	Tools          map[string]ProducerToolConfig
}

type ProducerToolConfig struct {
	Binary           string
	APIArgsAllowlist []string
}

type Deps struct {
	Store      *store.Store
	Sidecar    Sidecar
	Config     Config
	Now        func() time.Time
	ExecRunner producerExecRunner
}

type FailedItem struct {
	RelPath string `json:"rel_path"`
	Reason  string `json:"reason"`
}

type Progress struct {
	Current     int          `json:"current"`
	Total       int          `json:"total"`
	Msg         string       `json:"msg,omitempty"`
	Warnings    int          `json:"warnings"`
	FailedItems []FailedItem `json:"failed_items,omitempty"`
}

type itemHash struct {
	Type  string
	Value string
	Norm  string
}

type hashConflict struct {
	BlobIDA int64
	BlobIDB int64
	Reason  string
	Detail  string
}

type restoreResult struct {
	Result     *sidecarclient.ItemResult
	RemotePath string
	Reason     string
}

type manualRunner struct {
	store        *store.Store
	sidecar      Sidecar
	job          Job
	library      queries.Library
	account      queries.Account
	targetSubdir string
	now          func() time.Time
	progress     *progressTracker
	dedup        *deduper
}

func RunManual(ctx context.Context, job Job, deps Deps) error {
	if deps.Store == nil {
		return fmt.Errorf("ingest store is nil")
	}
	if deps.Sidecar == nil {
		return fmt.Errorf("ingest sidecar is nil")
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}

	library, err := deps.Store.GetLibrary(ctx, queries.GetLibraryParams{ID: job.LibraryID})
	if err != nil {
		return fmt.Errorf("load library %d: %w", job.LibraryID, err)
	}
	account, err := deps.Store.GetAccount(ctx, queries.GetAccountParams{ID: job.TargetAccount})
	if err != nil {
		return fmt.Errorf("load account %q: %w", job.TargetAccount, err)
	}
	targetSubdir, err := normalizeRelPath(job.TargetSubdir)
	if err != nil {
		return fmt.Errorf("invalid target_subdir: %w", err)
	}

	manifestFile, err := os.Open(job.ManifestPath)
	if err != nil {
		return fmt.Errorf("open manifest: %w", err)
	}
	defer manifestFile.Close()
	items, parseErrs, err := castree.ReadManifest(manifestFile)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	progress := newProgressTracker(deps.Store, job.ID, len(items)+len(parseErrs), progressInterval(deps.Config), now)
	for _, parseErr := range parseErrs {
		if err := progress.Failed(ctx, "", parseErr.Error()); err != nil {
			return err
		}
	}

	runner := &manualRunner{
		store:        deps.Store,
		sidecar:      deps.Sidecar,
		job:          job,
		library:      library,
		account:      account,
		targetSubdir: targetSubdir,
		now:          now,
		progress:     progress,
		dedup:        newDeduper(),
	}

	workerCount := deps.Config.WorkerPerJob
	if workerCount <= 0 {
		workerCount = 1
	}
	if workerCount > len(items) && len(items) > 0 {
		workerCount = len(items)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	itemCh := make(chan castree.Item)
	var wg sync.WaitGroup
	var fatalMu sync.Mutex
	var fatalErr error
	setFatal := func(err error) {
		if err == nil {
			return
		}
		fatalMu.Lock()
		defer fatalMu.Unlock()
		if fatalErr == nil {
			fatalErr = err
			cancel()
		}
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range itemCh {
				if runCtx.Err() != nil {
					return
				}
				if err := runner.processItem(runCtx, item); err != nil {
					setFatal(err)
					return
				}
			}
		}()
	}

	for _, item := range items {
		if runCtx.Err() != nil {
			break
		}
		select {
		case itemCh <- item:
		case <-runCtx.Done():
		}
	}
	close(itemCh)
	wg.Wait()

	if err := progress.Flush(ctx, true); err != nil {
		return err
	}

	fatalMu.Lock()
	defer fatalMu.Unlock()
	if fatalErr != nil {
		return fatalErr
	}
	return nil
}

func (r *manualRunner) processItem(ctx context.Context, item castree.Item) error {
	relPath, err := normalizeRelPath(item.RelPath)
	if err != nil {
		return r.progress.Failed(ctx, item.RelPath, "invalid rel_path: "+err.Error())
	}
	item.RelPath = relPath
	if item.Name == "" {
		item.Name = path.Base(relPath)
	}

	key := makeDedupKey(r.library.ID, item.RelPath, r.account.ID, r.account.StorageMount, r.targetSubdir)
	if !r.dedup.Add(key) {
		return r.progress.Done(ctx, "skipped duplicate item")
	}

	blobID, err := r.resolveCandidateAndHashes(ctx, item)
	if err != nil {
		return r.progress.Failed(ctx, item.RelPath, err.Error())
	}

	restore, terminated, err := restoreWithSidecar(ctx, r.sidecar, r.job.CASTreePath, r.account, item, r.targetSubdir)
	if err != nil {
		return err
	}
	if terminated {
		return r.progress.Failed(ctx, item.RelPath, restoreFailureReason(restore))
	}

	entry, terminated, reason, err := applyTwoPhaseCommit(ctx, r.store, r.library, r.account, item, blobID, restore, r.now)
	if err != nil {
		return r.progress.Failed(ctx, item.RelPath, err.Error())
	}
	if terminated {
		return r.progress.Failed(ctx, item.RelPath, reason)
	}

	if err := writeEchoFile(ctx, r.store, r.library, entry, item, r.now); err != nil {
		return r.progress.Failed(ctx, item.RelPath, err.Error())
	}
	return r.progress.Done(ctx, "ingested "+item.RelPath)
}

func (r *manualRunner) resolveCandidateAndHashes(ctx context.Context, item castree.Item) (int64, error) {
	tx, q, err := r.store.BeginImmediateTx(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin candidate transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	payload := payloadFromItem(item)
	blobID, _, err := resolveCandidateBlob(ctx, q, payload, r.now)
	if err != nil {
		return 0, err
	}
	if _, err := writeBlobHashes(ctx, q, blobID, payload, r.now); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit candidate transaction: %w", err)
	}
	committed = true
	return blobID, nil
}

func resolveCandidateBlob(ctx context.Context, q *queries.Queries, payload castree.Payload, now func() time.Time) (int64, []hashConflict, error) {
	hashes := hashesFromPayload(payload)
	type owner struct {
		blob queries.Blob
		hash itemHash
	}
	ownersByBlob := make(map[int64]owner)
	for _, hash := range hashes {
		existing, err := q.GetBlobHashByKey(ctx, queries.GetBlobHashByKeyParams{
			HashType:      hash.Type,
			HashValueNorm: hash.Norm,
			Size:          payload.Size,
		})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, nil, fmt.Errorf("lookup blob hash %s: %w", hash.Type, err)
		}
		if _, ok := ownersByBlob[existing.BlobID]; ok {
			continue
		}
		blob, err := q.GetBlob(ctx, queries.GetBlobParams{ID: existing.BlobID})
		if err != nil {
			return 0, nil, fmt.Errorf("load blob %d: %w", existing.BlobID, err)
		}
		ownersByBlob[existing.BlobID] = owner{blob: blob, hash: hash}
	}

	if len(ownersByBlob) == 0 {
		nowUnix := now().Unix()
		blob, err := q.CreateBlob(ctx, queries.CreateBlobParams{
			Size:          payload.Size,
			CanonicalName: sql.NullString{String: payload.Name, Valid: payload.Name != ""},
			SourceMtime:   parseSourceMtime(payload.CreateTime),
			OwnerID:       defaultOwnerID,
			CreatedAt:     nowUnix,
			UpdatedAt:     nowUnix,
		})
		if err != nil {
			return 0, nil, fmt.Errorf("create blob: %w", err)
		}
		return blob.ID, nil, nil
	}

	owners := make([]owner, 0, len(ownersByBlob))
	for _, owner := range ownersByBlob {
		owners = append(owners, owner)
	}
	sort.Slice(owners, func(i, j int) bool {
		if owners[i].blob.CreatedAt != owners[j].blob.CreatedAt {
			return owners[i].blob.CreatedAt < owners[j].blob.CreatedAt
		}
		return owners[i].blob.ID < owners[j].blob.ID
	})
	candidate := owners[0].blob.ID

	var conflicts []hashConflict
	for _, other := range owners[1:] {
		detail := mustJSON(map[string]any{
			"candidate_blob_id": candidate,
			"other_blob_id":     other.blob.ID,
			"hash_type":         other.hash.Type,
			"hash_value_norm":   other.hash.Norm,
			"size":              payload.Size,
			"name":              payload.Name,
		})
		if _, err := insertHashConflict(ctx, q, candidate, other.blob.ID, "hash_multi_blob", detail, now); err != nil {
			return 0, nil, err
		}
		conflicts = append(conflicts, hashConflict{BlobIDA: candidate, BlobIDB: other.blob.ID, Reason: "hash_multi_blob", Detail: detail})
	}
	return candidate, conflicts, nil
}

func writeBlobHashes(ctx context.Context, q *queries.Queries, blobID int64, payload castree.Payload, now func() time.Time) ([]hashConflict, error) {
	var conflicts []hashConflict
	for _, hash := range hashesFromPayload(payload) {
		existing, err := q.GetBlobHashByKey(ctx, queries.GetBlobHashByKeyParams{
			HashType:      hash.Type,
			HashValueNorm: hash.Norm,
			Size:          payload.Size,
		})
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := q.InsertBlobHash(ctx, queries.InsertBlobHashParams{
				BlobID:        blobID,
				HashType:      hash.Type,
				HashValue:     hash.Value,
				HashValueNorm: hash.Norm,
				Size:          payload.Size,
			}); err == nil {
				continue
			}

			existing, err = q.GetBlobHashByKey(ctx, queries.GetBlobHashByKeyParams{
				HashType:      hash.Type,
				HashValueNorm: hash.Norm,
				Size:          payload.Size,
			})
			if err != nil {
				return nil, fmt.Errorf("insert blob hash %s: %w", hash.Type, err)
			}
		} else if err != nil {
			return nil, fmt.Errorf("lookup blob hash %s: %w", hash.Type, err)
		}

		if existing.BlobID == blobID {
			continue
		}
		detail := mustJSON(map[string]any{
			"candidate_blob_id": blobID,
			"owner_blob_id":     existing.BlobID,
			"hash_type":         hash.Type,
			"hash_value_norm":   hash.Norm,
			"size":              payload.Size,
			"name":              payload.Name,
		})
		if _, err := insertHashConflict(ctx, q, blobID, existing.BlobID, "hash_owned_by_other_blob", detail, now); err != nil {
			return nil, err
		}
		conflicts = append(conflicts, hashConflict{BlobIDA: blobID, BlobIDB: existing.BlobID, Reason: "hash_owned_by_other_blob", Detail: detail})
	}
	return conflicts, nil
}

func restoreWithSidecar(ctx context.Context, sidecar Sidecar, treeRoot string, account queries.Account, item castree.Item, targetSubdir string) (*restoreResult, bool, error) {
	if err := pathsafe.ValidateRelPath(targetSubdir); err != nil {
		return &restoreResult{Reason: "invalid target_subdir: " + err.Error()}, true, nil
	}
	if err := pathsafe.ValidateRelPath(item.RelPath); err != nil {
		return &restoreResult{Reason: "invalid rel_path: " + err.Error()}, true, nil
	}

	casPath, err := castree.LocateCASFile(treeRoot, item.RelPath)
	if err != nil {
		return &restoreResult{Reason: err.Error()}, true, nil
	}
	casFile, err := os.Open(casPath)
	if err != nil {
		return &restoreResult{Reason: "open cas file: " + err.Error()}, true, nil
	}
	defer casFile.Close()
	stat, err := casFile.Stat()
	if err != nil {
		return &restoreResult{Reason: "stat cas file: " + err.Error()}, true, nil
	}

	remoteDir := remoteDirFor(targetSubdir, item.RelPath)
	req := sidecarclient.PutCASRequest{
		StorageMount: account.StorageMount,
		RemoteDir:    remoteDir,
		CASName:      path.Base(item.RelPath) + ".cas",
		CASBody:      casFile,
		CASSize:      stat.Size(),
	}
	result, err := sidecar.PutCAS(ctx, req)
	if err != nil {
		if errors.Is(err, sidecarclient.ErrSidecarUnreachable) {
			return nil, false, err
		}
		return &restoreResult{Reason: err.Error()}, true, nil
	}
	if result == nil {
		return &restoreResult{Reason: "sidecar returned empty result"}, true, nil
	}

	switch result.Status {
	case sidecarclient.StatusRestored, sidecarclient.StatusSkippedDup:
		remotePath := result.CloudPath
		if remotePath == "" {
			remotePath = path.Join(req.RemoteDir, strings.TrimSuffix(req.CASName, ".cas"))
		}
		if !strings.HasPrefix(remotePath, "/") {
			remotePath = "/" + strings.TrimPrefix(remotePath, "/")
		}
		return &restoreResult{Result: result, RemotePath: path.Clean(remotePath)}, false, nil
	case sidecarclient.StatusFailed:
		reason := result.Error
		if reason == "" {
			reason = "sidecar restore failed"
		}
		return &restoreResult{Result: result, Reason: reason}, true, nil
	default:
		return &restoreResult{Result: result, Reason: "unexpected sidecar restore status: " + result.Status}, true, nil
	}
}

func applyTwoPhaseCommit(ctx context.Context, st *store.Store, library queries.Library, account queries.Account, item castree.Item, blobID int64, restore *restoreResult, now func() time.Time) (queries.LibraryEntry, bool, string, error) {
	tx, q, err := st.BeginImmediateTx(ctx)
	if err != nil {
		return queries.LibraryEntry{}, false, "", fmt.Errorf("begin ingest commit transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	nowUnix := now().Unix()
	existing, err := q.GetFileCopyByRemotePath(ctx, queries.GetFileCopyByRemotePathParams{
		SidecarID:    account.SidecarID,
		StorageMount: account.StorageMount,
		RemotePath:   restore.RemotePath,
	})
	if err == nil {
		if existing.BlobID != blobID {
			detail := mustJSON(map[string]any{
				"sidecar_id":        account.SidecarID,
				"storage_mount":     account.StorageMount,
				"remote_path":       restore.RemotePath,
				"manifest_rel_path": item.RelPath,
				"existing_blob_id":  existing.BlobID,
				"candidate_blob_id": blobID,
			})
			if _, err := insertHashConflict(ctx, q, existing.BlobID, blobID, "copy_blob_mismatch", detail, now); err != nil {
				return queries.LibraryEntry{}, false, "", err
			}
			if err := tx.Commit(ctx); err != nil {
				return queries.LibraryEntry{}, false, "", fmt.Errorf("commit copy mismatch conflict: %w", err)
			}
			committed = true
			return queries.LibraryEntry{}, true, "copy_blob_mismatch", nil
		}
		if err := q.UpdateFileCopyLive(ctx, queries.UpdateFileCopyLiveParams{
			LastSeen:    nowUnix,
			CloudFileID: nullableResultValue(restore.Result, "cloud_file_id"),
			Pickcode:    nullableResultValue(restore.Result, "pickcode"),
			ID:          existing.ID,
		}); err != nil {
			return queries.LibraryEntry{}, false, "", fmt.Errorf("update file copy: %w", err)
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, err := q.InsertFileCopy(ctx, queries.InsertFileCopyParams{
			BlobID:       blobID,
			Provider:     account.Provider,
			AccountID:    account.ID,
			SidecarID:    account.SidecarID,
			StorageMount: account.StorageMount,
			RemotePath:   restore.RemotePath,
			CloudFileID:  nullableResultValue(restore.Result, "cloud_file_id"),
			Pickcode:     nullableResultValue(restore.Result, "pickcode"),
			Status:       "live",
			LastSeen:     nowUnix,
		}); err != nil {
			return queries.LibraryEntry{}, false, "", fmt.Errorf("insert file copy: %w", err)
		}
	} else {
		return queries.LibraryEntry{}, false, "", fmt.Errorf("lookup file copy: %w", err)
	}

	entry, err := q.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   library.ID,
		RelPath:     item.RelPath,
		Name:        item.Name,
		BlobID:      blobID,
		EchoWritten: 0,
		CreatedAt:   nowUnix,
		UpdatedAt:   nowUnix,
	})
	if err != nil {
		return queries.LibraryEntry{}, false, "", fmt.Errorf("upsert library entry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return queries.LibraryEntry{}, false, "", fmt.Errorf("commit ingest db phase: %w", err)
	}
	committed = true
	return entry, false, "", nil
}

func writeEchoFile(ctx context.Context, st *store.Store, library queries.Library, entry queries.LibraryEntry, item castree.Item, now func() time.Time) error {
	final, err := pathsafe.SafeJoinUnderLibrary(library.EchoOutputPath, item.RelPath)
	if err != nil {
		return fmt.Errorf("prepare echo output: %w", err)
	}
	encoded, err := castree.Encode(payloadFromItem(item))
	if err != nil {
		return fmt.Errorf("encode echo payload: %w", err)
	}
	if err := echofile.PutAtomic(final, encoded); err != nil {
		return err
	}
	if err := st.MarkLibraryEntryEchoWritten(ctx, queries.MarkLibraryEntryEchoWrittenParams{
		UpdatedAt: now().Unix(),
		ID:        entry.ID,
	}); err != nil {
		return fmt.Errorf("mark echo written: %w", err)
	}
	return nil
}

func insertHashConflict(ctx context.Context, q *queries.Queries, blobIDA, blobIDB int64, reason, detail string, now func() time.Time) (queries.HashConflict, error) {
	if blobIDA == blobIDB {
		return queries.HashConflict{}, nil
	}
	conflict, err := q.InsertHashConflict(ctx, queries.InsertHashConflictParams{
		BlobIDA:    blobIDA,
		BlobIDB:    blobIDB,
		Reason:     reason,
		Detail:     detail,
		ObservedAt: now().Unix(),
		Status:     "open",
	})
	if err != nil {
		return queries.HashConflict{}, fmt.Errorf("insert hash conflict %s: %w", reason, err)
	}
	return conflict, nil
}

func payloadFromItem(item castree.Item) castree.Payload {
	name := item.Name
	if name == "" && item.RelPath != "" {
		name = path.Base(item.RelPath)
	}
	return castree.Payload{
		Name:       name,
		Size:       item.Size,
		Provider:   item.Provider,
		SHA1:       item.SHA1,
		PreID:      item.PreID,
		MD5:        item.MD5,
		SliceMD5:   item.SliceMD5,
		SHA256:     item.SHA256,
		CreateTime: item.CreateTime,
	}
}

func hashesFromPayload(payload castree.Payload) []itemHash {
	candidates := []itemHash{
		{Type: "sha1", Value: payload.SHA1},
		{Type: "md5", Value: payload.MD5},
		{Type: "sha256", Value: payload.SHA256},
		{Type: "preid", Value: payload.PreID},
		{Type: "slice_md5", Value: payload.SliceMD5},
	}
	out := make([]itemHash, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.Value = strings.TrimSpace(candidate.Value)
		if candidate.Value == "" {
			continue
		}
		candidate.Norm = normalizeHash(candidate.Value)
		out = append(out, candidate)
	}
	return out
}

func normalizeHash(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(":", "", "-", "", " ", "")
	return replacer.Replace(value)
}

func normalizeRelPath(rel string) (string, error) {
	if err := pathsafe.ValidateRelPath(rel); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Clean(rel)), nil
}

func remoteDirFor(targetSubdir, relPath string) string {
	dir := path.Dir(relPath)
	if dir == "." {
		return "/" + strings.TrimPrefix(path.Clean(targetSubdir), "/")
	}
	return "/" + strings.TrimPrefix(path.Join(targetSubdir, dir), "/")
}

func nullableResultValue(result *sidecarclient.ItemResult, key string) sql.NullString {
	if result == nil || result.Hashes == nil || result.Hashes[key] == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: result.Hashes[key], Valid: true}
}

func restoreFailureReason(result *restoreResult) string {
	if result == nil || result.Reason == "" {
		return "sidecar restore failed"
	}
	return result.Reason
}

func parseSourceMtime(value string) sql.NullInt64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return sql.NullInt64{}
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return sql.NullInt64{Int64: parsed.Unix(), Valid: true}
	}
	return sql.NullInt64{}
}

func mustJSON(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		return `{}`
	}
	return string(body)
}

func progressInterval(cfg Config) time.Duration {
	if cfg.ProgressInterval > 0 {
		return cfg.ProgressInterval
	}
	return defaultProgressInterval
}

type progressTracker struct {
	mu       sync.Mutex
	store    *store.Store
	jobID    int64
	interval time.Duration
	now      func() time.Time
	last     time.Time
	value    Progress
}

func newProgressTracker(st *store.Store, jobID int64, total int, interval time.Duration, now func() time.Time) *progressTracker {
	return &progressTracker{
		store:    st,
		jobID:    jobID,
		interval: interval,
		now:      now,
		value: Progress{
			Total: total,
		},
	}
}

func (p *progressTracker) Done(ctx context.Context, msg string) error {
	p.mu.Lock()
	p.value.Current++
	p.value.Msg = msg
	err := p.flushLocked(ctx, false)
	p.mu.Unlock()
	return err
}

func (p *progressTracker) Failed(ctx context.Context, relPath, reason string) error {
	p.mu.Lock()
	p.value.Current++
	p.value.Msg = reason
	p.value.Warnings++
	p.value.FailedItems = append(p.value.FailedItems, FailedItem{RelPath: relPath, Reason: reason})
	err := p.flushLocked(ctx, false)
	p.mu.Unlock()
	return err
}

func (p *progressTracker) Flush(ctx context.Context, force bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.flushLocked(ctx, force)
}

func (p *progressTracker) flushLocked(ctx context.Context, force bool) error {
	if p.store == nil || p.jobID == 0 {
		return nil
	}
	now := p.now()
	if !force && !p.last.IsZero() && now.Sub(p.last) < p.interval {
		return nil
	}
	body, err := json.Marshal(p.value)
	if err != nil {
		return err
	}
	if err := p.store.UpdateJobProgress(ctx, queries.UpdateJobProgressParams{
		ID:       p.jobID,
		Progress: sql.NullString{String: string(body), Valid: true},
	}); err != nil {
		return fmt.Errorf("update job progress: %w", err)
	}
	p.last = now
	return nil
}
