package castree

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLocateCASFileFindsNestedRelPath(t *testing.T) {
	root := t.TempDir()
	casPath := filepath.Join(root, "Season 1", "E01.mkv.cas")
	if err := os.MkdirAll(filepath.Dir(casPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(casPath, []byte("cas"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LocateCASFile(root, "Season 1/E01.mkv")
	if err != nil {
		t.Fatalf("LocateCASFile() error = %v", err)
	}
	if got != casPath {
		t.Fatalf("LocateCASFile() = %q, want %q", got, casPath)
	}
}

func TestLocateCASFileMissingReturnsExplicitError(t *testing.T) {
	_, err := LocateCASFile(t.TempDir(), "missing.mkv")
	if !errors.Is(err, ErrCASFileNotFound) {
		t.Fatalf("LocateCASFile() error = %v, want ErrCASFileNotFound", err)
	}
}

func TestLocateCASFileRejectsUnsafeRelPath(t *testing.T) {
	tests := []string{
		"",
		"../outside.mkv",
		"dir/../outside.mkv",
		"/absolute.mkv",
		`dir\file.mkv`,
		"C:/windows.mkv",
		"\tfile.mkv",
		"file.mkv\n",
		"bad\x00name.mkv",
	}

	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			_, err := LocateCASFile(t.TempDir(), tt)
			if !errors.Is(err, ErrCASTreeInvalid) {
				t.Fatalf("LocateCASFile(%q) error = %v, want ErrCASTreeInvalid", tt, err)
			}
		})
	}
}

func TestLocateCASFileRejectsNonDirectoryRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root.txt")
	if err := os.WriteFile(root, []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LocateCASFile(root, "a.mkv")
	if !errors.Is(err, ErrCASTreeInvalid) {
		t.Fatalf("LocateCASFile() error = %v, want ErrCASTreeInvalid", err)
	}
}

func TestLocateCASFileRejectsSymlinkCASFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.cas")
	if err := os.WriteFile(target, []byte("cas"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.mkv.cas")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	_, err := LocateCASFile(root, "link.mkv")
	if !errors.Is(err, ErrCASTreeInvalid) {
		t.Fatalf("LocateCASFile() error = %v, want ErrCASTreeInvalid", err)
	}
}

func TestLocateCASFileRejectsSymlinkParentEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "file.mkv.cas"), []byte("cas"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}

	_, err := LocateCASFile(root, "linked/file.mkv")
	if !errors.Is(err, ErrCASTreeInvalid) {
		t.Fatalf("LocateCASFile() error = %v, want ErrCASTreeInvalid", err)
	}
}

func TestLocateCASFileRejectsNonRegularCASFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir.mkv.cas"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := LocateCASFile(root, "dir.mkv")
	if !errors.Is(err, ErrCASTreeInvalid) {
		t.Fatalf("LocateCASFile() error = %v, want ErrCASTreeInvalid", err)
	}
}
