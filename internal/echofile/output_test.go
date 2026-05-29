package echofile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPutAtomicWritesFinalAndRemovesTmp(t *testing.T) {
	root := t.TempDir()
	final := filepath.Join(root, "movie.echo")
	payload := []byte("payload")

	if err := PutAtomic(final, payload); err != nil {
		t.Fatalf("PutAtomic() error = %v", err)
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("final payload = %q, want %q", got, payload)
	}
	if _, err := os.Stat(final + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp exists after PutAtomic, stat err = %v", err)
	}
}

func TestPutAtomicReplacesExistingFile(t *testing.T) {
	root := t.TempDir()
	final := filepath.Join(root, "movie.echo")
	if err := os.WriteFile(final, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PutAtomic(final, []byte("new")); err != nil {
		t.Fatalf("PutAtomic() error = %v", err)
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("final payload = %q, want new", got)
	}
}

func TestPutAtomicReplacesStaleTmp(t *testing.T) {
	root := t.TempDir()
	final := filepath.Join(root, "movie.echo")
	if err := os.WriteFile(final+".tmp", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PutAtomic(final, []byte("new")); err != nil {
		t.Fatalf("PutAtomic(existing tmp) error = %v", err)
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("final payload = %q, want new", got)
	}
}

func TestPutAtomicRejectsSymlinkTmp(t *testing.T) {
	root := t.TempDir()
	final := filepath.Join(root, "movie.echo")
	target := filepath.Join(root, "target.tmp")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, final+".tmp"); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := PutAtomic(final, []byte("new")); err == nil {
		t.Fatal("PutAtomic(symlink tmp) error = nil, want error")
	}
}

func TestPutAtomicRejectsDirectoryTmp(t *testing.T) {
	root := t.TempDir()
	final := filepath.Join(root, "movie.echo")
	if err := os.Mkdir(final+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PutAtomic(final, []byte("new")); err == nil {
		t.Fatal("PutAtomic(directory tmp) error = nil, want error")
	}
}

func TestRemoveTmpDeletesEchoTmpOnly(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "a.echo.tmp"),
		filepath.Join(root, "nested", "b.echo.tmp"),
	}
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("tmp"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(root, "keep.tmp")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveTmp(root)
	if err != nil {
		t.Fatalf("RemoveTmp() error = %v", err)
	}
	if len(removed) != len(paths) {
		t.Fatalf("RemoveTmp removed %d paths, want %d: %#v", len(removed), len(paths), removed)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists, stat err = %v", p, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("keep file missing: %v", err)
	}
}
