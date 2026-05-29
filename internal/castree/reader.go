package castree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrManifestInvalid = errors.New("manifest unreadable or malformed")
	ErrCASTreeInvalid  = errors.New("cas tree missing or unreadable")
	ErrCASFileNotFound = errors.New("cas file not found")
)

func LocateCASFile(treeRoot, relPath string) (string, error) {
	cleanRel, err := cleanRelPath(relPath)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCASTreeInvalid, err)
	}

	root, err := filepath.Abs(treeRoot)
	if err != nil {
		return "", fmt.Errorf("%w: resolve root: %v", ErrCASTreeInvalid, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("%w: stat root: %v", ErrCASTreeInvalid, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: root is not a directory", ErrCASTreeInvalid)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve root symlinks: %v", ErrCASTreeInvalid, err)
	}

	candidate := filepath.Join(root, filepath.FromSlash(cleanRel+".cas"))
	if !pathWithin(root, candidate) {
		return "", fmt.Errorf("%w: %q escapes tree root", ErrCASTreeInvalid, relPath)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrCASFileNotFound, cleanRel+".cas")
		}
		return "", fmt.Errorf("%w: resolve cas parent: %v", ErrCASTreeInvalid, err)
	}
	if !pathWithin(root, parent) {
		return "", fmt.Errorf("%w: %q resolves outside tree root", ErrCASTreeInvalid, relPath)
	}
	candidate = filepath.Join(parent, filepath.Base(candidate))

	info, err = os.Lstat(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrCASFileNotFound, cleanRel+".cas")
		}
		return "", fmt.Errorf("%w: stat cas file: %v", ErrCASTreeInvalid, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %s is a symlink", ErrCASTreeInvalid, cleanRel+".cas")
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s is not a regular file", ErrCASTreeInvalid, cleanRel+".cas")
	}
	return candidate, nil
}

func cleanRelPath(relPath string) (string, error) {
	rel := filepath.ToSlash(relPath)
	if rel == "" {
		return "", fmt.Errorf("empty relative path")
	}
	if len(rel) > 4096 {
		return "", fmt.Errorf("relative path too long")
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("absolute path %q", relPath)
	}
	if strings.Contains(relPath, `\`) {
		return "", fmt.Errorf("backslash in relative path %q", relPath)
	}
	if len(rel) >= 2 && rel[1] == ':' {
		return "", fmt.Errorf("windows drive path %q", relPath)
	}
	for _, r := range rel {
		if r == 0 || r < 0x20 {
			return "", fmt.Errorf("control character in relative path")
		}
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", fmt.Errorf("parent segment in relative path %q", relPath)
		}
	}

	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "." || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("invalid relative path %q", relPath)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", fmt.Errorf("parent segment in relative path %q", relPath)
		}
		if len(part) >= 2 && part[1] == ':' {
			return "", fmt.Errorf("windows drive path %q", relPath)
		}
		for _, r := range part {
			if r == 0 || r < 0x20 {
				return "", fmt.Errorf("control character in relative path")
			}
		}
	}
	return clean, nil
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
