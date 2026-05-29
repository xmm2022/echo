package ingest

import (
	"path"
	"sync"
)

type dedupKey struct {
	libraryID       int64
	relPath         string
	accountID       string
	storageMount    string
	targetRemoteDir string
	basename        string
}

type deduper struct {
	mu   sync.Mutex
	seen map[dedupKey]struct{}
}

func newDeduper() *deduper {
	return &deduper{seen: make(map[dedupKey]struct{})}
}

func (d *deduper) Add(key dedupKey) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok {
		return false
	}
	d.seen[key] = struct{}{}
	return true
}

func makeDedupKey(libraryID int64, relPath, accountID, storageMount, targetSubdir string) dedupKey {
	dir := path.Dir(relPath)
	targetRemoteDir := path.Clean(targetSubdir)
	if dir != "." {
		targetRemoteDir = path.Join(targetRemoteDir, dir)
	}
	return dedupKey{
		libraryID:       libraryID,
		relPath:         relPath,
		accountID:       accountID,
		storageMount:    storageMount,
		targetRemoteDir: targetRemoteDir,
		basename:        path.Base(relPath),
	}
}
