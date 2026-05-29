package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmm2022/echo/internal/castree"
	"github.com/xmm2022/echo/internal/echofile"
	"github.com/xmm2022/echo/internal/pathsafe"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

const reconcileBatchSize = 100

func Reconcile(ctx context.Context, st *store.Store, logger *slog.Logger) error {
	if st == nil {
		return fmt.Errorf("reconcile store is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}

	libraries, err := st.ListLibraries(ctx)
	if err != nil {
		return fmt.Errorf("list libraries: %w", err)
	}
	for _, library := range libraries {
		removed, err := echofile.RemoveTmp(library.EchoOutputPath)
		if err != nil {
			return fmt.Errorf("remove echo tmp files for library %d: %w", library.ID, err)
		}
		for _, path := range removed {
			logger.Warn("removed stale echo tmp", "path", path, "library_id", library.ID)
		}
	}

	for {
		entries, err := st.ListLibraryEntriesNeedingEcho(ctx, queries.ListLibraryEntriesNeedingEchoParams{Limit: reconcileBatchSize})
		if err != nil {
			return fmt.Errorf("list entries needing echo: %w", err)
		}
		if len(entries) == 0 {
			break
		}
		for _, entry := range entries {
			if err := reconcileEntry(ctx, st, entry, time.Now); err != nil {
				return err
			}
		}
	}

	for _, library := range libraries {
		if err := warnOrphanEchoFiles(ctx, st, logger, library); err != nil {
			return err
		}
	}
	return nil
}

func reconcileEntry(ctx context.Context, st *store.Store, entry queries.LibraryEntry, now func() time.Time) error {
	library, err := st.GetLibrary(ctx, queries.GetLibraryParams{ID: entry.LibraryID})
	if err != nil {
		return fmt.Errorf("load reconcile library %d: %w", entry.LibraryID, err)
	}
	blob, err := st.GetBlob(ctx, queries.GetBlobParams{ID: entry.BlobID})
	if err != nil {
		return fmt.Errorf("load reconcile blob %d: %w", entry.BlobID, err)
	}
	hashes, err := st.ListBlobHashesByBlob(ctx, queries.ListBlobHashesByBlobParams{BlobID: entry.BlobID})
	if err != nil {
		return fmt.Errorf("list reconcile blob hashes %d: %w", entry.BlobID, err)
	}

	final, err := pathsafe.SafeJoinUnderLibrary(library.EchoOutputPath, entry.RelPath)
	if err != nil {
		return fmt.Errorf("prepare reconcile echo output: %w", err)
	}

	if body, err := os.ReadFile(final); err == nil {
		payload, decodeErr := castree.Decode(body)
		if decodeErr == nil && echoPayloadMatchesDB(payload, blob, hashes) {
			return st.MarkLibraryEntryEchoWritten(ctx, queries.MarkLibraryEntryEchoWrittenParams{
				UpdatedAt: now().Unix(),
				ID:        entry.ID,
			})
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read existing echo file: %w", err)
	}

	payload := payloadFromDB(entry, blob, hashes)
	encoded, err := castree.Encode(payload)
	if err != nil {
		return fmt.Errorf("encode reconcile echo payload: %w", err)
	}
	if err := echofile.PutAtomic(final, encoded); err != nil {
		return err
	}
	if err := st.MarkLibraryEntryEchoWritten(ctx, queries.MarkLibraryEntryEchoWrittenParams{
		UpdatedAt: now().Unix(),
		ID:        entry.ID,
	}); err != nil {
		return fmt.Errorf("mark reconcile echo written: %w", err)
	}
	return nil
}

func warnOrphanEchoFiles(ctx context.Context, st *store.Store, logger *slog.Logger, library queries.Library) error {
	root := library.EchoOutputPath
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(p) != ".echo" {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = strings.TrimSuffix(filepath.ToSlash(rel), ".echo")
		entry, err := st.GetLibraryEntry(ctx, queries.GetLibraryEntryParams{
			LibraryID: library.ID,
			RelPath:   rel,
		})
		if errors.Is(err, sql.ErrNoRows) {
			logger.Warn("orphan echo file has no library entry", "path", p, "library_id", library.ID)
			return nil
		}
		if err != nil {
			return err
		}
		copies, err := st.ListLiveCopiesByBlob(ctx, queries.ListLiveCopiesByBlobParams{
			BlobID: entry.BlobID,
			Limit:  1,
		})
		if err != nil {
			return err
		}
		if len(copies) == 0 {
			logger.Warn("orphan echo file has no live copy", "path", p, "library_id", library.ID, "entry_id", entry.ID)
		}
		return nil
	})
}

func echoPayloadMatchesDB(payload castree.Payload, blob queries.Blob, hashes []queries.BlobHash) bool {
	if payload.Size != blob.Size {
		return false
	}
	for _, hash := range hashes {
		value, ok := payloadHashValue(payload, hash.HashType)
		if !ok || normalizeHash(value) != hash.HashValueNorm {
			return false
		}
	}
	return true
}

func payloadFromDB(entry queries.LibraryEntry, blob queries.Blob, hashes []queries.BlobHash) castree.Payload {
	payload := castree.Payload{
		Name: entry.Name,
		Size: blob.Size,
	}
	for _, hash := range hashes {
		switch hash.HashType {
		case "sha1":
			if payload.SHA1 == "" {
				payload.SHA1 = hash.HashValue
			}
		case "md5":
			if payload.MD5 == "" {
				payload.MD5 = hash.HashValue
			}
		case "sha256":
			if payload.SHA256 == "" {
				payload.SHA256 = hash.HashValue
			}
		case "preid":
			if payload.PreID == "" {
				payload.PreID = hash.HashValue
			}
		case "slice_md5":
			if payload.SliceMD5 == "" {
				payload.SliceMD5 = hash.HashValue
			}
		}
	}
	return payload
}

func payloadHashValue(payload castree.Payload, hashType string) (string, bool) {
	switch hashType {
	case "sha1":
		return payload.SHA1, payload.SHA1 != ""
	case "md5":
		return payload.MD5, payload.MD5 != ""
	case "sha256":
		return payload.SHA256, payload.SHA256 != ""
	case "preid":
		return payload.PreID, payload.PreID != ""
	case "slice_md5":
		return payload.SliceMD5, payload.SliceMD5 != ""
	default:
		return "", false
	}
}
