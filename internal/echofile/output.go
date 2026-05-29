package echofile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func PutAtomic(final string, payload []byte) error {
	tmp := final + ".tmp"
	if err := removeStaleTmp(tmp); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open echo tmp file: %w", err)
	}
	writeErr := func() error {
		if _, err := f.Write(payload); err != nil {
			return fmt.Errorf("write echo tmp file: %w", err)
		}
		if err := f.Sync(); err != nil {
			return fmt.Errorf("sync echo tmp file: %w", err)
		}
		return nil
	}()
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(tmp)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close echo tmp file: %w", closeErr)
	}

	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename echo tmp file: %w", err)
	}
	if err := syncDir(filepath.Dir(final)); err != nil {
		return err
	}
	return nil
}

func removeStaleTmp(tmp string) error {
	info, err := os.Lstat(tmp)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lstat echo tmp file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("echo tmp file is a symlink")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("echo tmp path exists and is not a regular file")
	}
	if err := os.Remove(tmp); err != nil {
		return fmt.Errorf("remove stale echo tmp file: %w", err)
	}
	return nil
}

func RemoveTmp(libraryRoot string) ([]string, error) {
	var removed []string
	err := filepath.WalkDir(libraryRoot, func(path string, d fs.DirEntry, err error) error {
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
		if filepath.Ext(path) != ".tmp" || filepath.Ext(path[:len(path)-len(".tmp")]) != ".echo" {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		removed = append(removed, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("remove echo tmp files: %w", err)
	}
	return removed, nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open echo parent directory: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync echo parent directory: %w", err)
	}
	return nil
}
