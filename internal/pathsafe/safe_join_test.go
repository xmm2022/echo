package pathsafe

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHelpersRejectBadRoots(t *testing.T) {
	tmp := t.TempDir()
	fileRoot := filepath.Join(tmp, "file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingRoot := filepath.Join(tmp, "missing")

	tests := []struct {
		name string
		root string
	}{
		{name: "relative", root: "relative-root"},
		{name: "empty", root: ""},
		{name: "missing", root: missingRoot},
		{name: "file", root: fileRoot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ResolveExistingUnderRoot(tt.root, "x"); err == nil {
				t.Fatal("ResolveExistingUnderRoot error = nil, want error")
			}
			if _, err := PrepareOutputUnderRoot(tt.root, "x", ".echo"); err == nil {
				t.Fatal("PrepareOutputUnderRoot error = nil, want error")
			}
			if _, err := PrepareNewDirUnderRoot(tt.root, "x"); err == nil {
				t.Fatal("PrepareNewDirUnderRoot error = nil, want error")
			}
		})
	}
}

func TestHelpersAllowSymlinkRootToDirectory(t *testing.T) {
	tmp := t.TempDir()
	realRoot := filepath.Join(tmp, "real")
	linkRoot := filepath.Join(tmp, "link")
	target := filepath.Join(realRoot, "x")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	if got, err := ResolveExistingUnderRoot(linkRoot, "x"); err != nil || got != target {
		t.Fatalf("ResolveExistingUnderRoot(symlink root) = %q, %v, want %q", got, err, target)
	}
	if got, err := PrepareOutputUnderRoot(linkRoot, "out", ".echo"); err != nil || got != filepath.Join(realRoot, "out.echo") {
		t.Fatalf("PrepareOutputUnderRoot(symlink root) = %q, %v", got, err)
	}
	if got, err := PrepareNewDirUnderRoot(linkRoot, "job"); err != nil || got != filepath.Join(realRoot, "job") {
		t.Fatalf("PrepareNewDirUnderRoot(symlink root) = %q, %v", got, err)
	}
}

func TestResolveExistingUnderRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b.txt")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveExistingUnderRoot(root, "a/b.txt")
	if err != nil {
		t.Fatalf("ResolveExistingUnderRoot() error = %v", err)
	}
	if got != nested {
		t.Fatalf("ResolveExistingUnderRoot() = %q, want %q", got, nested)
	}

	got, err = ResolveExistingUnderRoot(root, nested)
	if err != nil {
		t.Fatalf("ResolveExistingUnderRoot(abs) error = %v", err)
	}
	if got != nested {
		t.Fatalf("ResolveExistingUnderRoot(abs) = %q, want %q", got, nested)
	}

	got, err = ResolveExistingUnderRoot(root, "./a//b.txt")
	if err != nil {
		t.Fatalf("ResolveExistingUnderRoot(clean rel) error = %v", err)
	}
	if got != nested {
		t.Fatalf("ResolveExistingUnderRoot(clean rel) = %q, want %q", got, nested)
	}

	got, err = ResolveExistingUnderRoot(root, root)
	if err != nil {
		t.Fatalf("ResolveExistingUnderRoot(root) error = %v", err)
	}
	if got != root {
		t.Fatalf("ResolveExistingUnderRoot(root) = %q, want %q", got, root)
	}
}

func TestResolveExistingUnderRootRejectsMissingAndEscape(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	outside := filepath.Join(tmp, "outside.txt")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveExistingUnderRoot(root, "missing"); err == nil {
		t.Fatal("ResolveExistingUnderRoot(missing) error = nil, want error")
	}
	if _, err := ResolveExistingUnderRoot(root, outside); err == nil {
		t.Fatal("ResolveExistingUnderRoot(outside) error = nil, want error")
	}
	absOutsideDir := filepath.Join(tmp, "outside-dir")
	if err := os.Mkdir(absOutsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveExistingUnderRoot(root, absOutsideDir); err == nil {
		t.Fatal("ResolveExistingUnderRoot(outside dir) error = nil, want error")
	}
	if _, err := ResolveExistingUnderRoot(root, ""); err == nil {
		t.Fatal("ResolveExistingUnderRoot(empty) error = nil, want error")
	}
}

func TestResolveExistingUnderRootRejectsSymlink(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	outside := filepath.Join(tmp, "outside")
	target := filepath.Join(outside, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	if _, err := ResolveExistingUnderRoot(root, "link.txt"); err == nil {
		t.Fatal("ResolveExistingUnderRoot(symlink) error = nil, want error")
	}
}

func TestResolveExistingUnderRootRejectsSymlinkParentEscape(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	outside := filepath.Join(tmp, "outside")
	outsideFile := filepath.Join(outside, "file.txt")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	if _, err := ResolveExistingUnderRoot(root, "link/file.txt"); err == nil {
		t.Fatal("ResolveExistingUnderRoot(symlink parent escape) error = nil, want error")
	}
}

func TestResolveExistingUnderRootRejectsSymlinkParentInsideRoot(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	target := filepath.Join(realDir, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(root, "link-dir")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	if _, err := ResolveExistingUnderRoot(root, filepath.Join("link-dir", "target.txt")); err == nil {
		t.Fatal("ResolveExistingUnderRoot(symlink parent inside root) error = nil, want error")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveExistingUnderRoot(root, "link.txt"); err == nil {
		t.Fatal("ResolveExistingUnderRoot(final symlink inside root) error = nil, want error")
	}
}

func TestResolveExistingUnderRootRejectsAbsoluteSymlinkParentBeforeDotDot(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	otherDir := filepath.Join(root, "other")
	otherFile := filepath.Join(otherDir, "file.txt")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(root, "link")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	candidate := root + string(filepath.Separator) + "link" + string(filepath.Separator) + ".." + string(filepath.Separator) + "other" + string(filepath.Separator) + "file.txt"
	if _, err := ResolveExistingUnderRoot(root, candidate); err == nil {
		t.Fatal("ResolveExistingUnderRoot(absolute symlink parent before ..) error = nil, want error")
	}
}

func TestPrepareOutputUnderRootCreatesParentAndAllowsRegularFinal(t *testing.T) {
	root := t.TempDir()
	final, err := PrepareOutputUnderRoot(root, "a/b/movie.mkv", ".echo")
	if err != nil {
		t.Fatalf("PrepareOutputUnderRoot() error = %v", err)
	}
	want := filepath.Join(root, "a", "b", "movie.mkv.echo")
	if final != want {
		t.Fatalf("PrepareOutputUnderRoot() = %q, want %q", final, want)
	}
	if info, err := os.Stat(filepath.Dir(final)); err != nil || !info.IsDir() {
		t.Fatalf("parent was not created as directory: info=%v err=%v", info, err)
	}
	if err := os.WriteFile(final, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := PrepareOutputUnderRoot(root, "a/b/movie.mkv", ".echo"); err != nil || got != final {
		t.Fatalf("PrepareOutputUnderRoot(existing regular) = %q, %v", got, err)
	}

	cleaned, err := PrepareOutputUnderRoot(root, "./clean//name", ".echo")
	if err != nil {
		t.Fatalf("PrepareOutputUnderRoot(clean rel) error = %v", err)
	}
	cleanWant := filepath.Join(root, "clean", "name.echo")
	if cleaned != cleanWant {
		t.Fatalf("PrepareOutputUnderRoot(clean rel) = %q, want %q", cleaned, cleanWant)
	}
}

func TestSafeJoinUnderLibraryUsesEchoSuffix(t *testing.T) {
	root := t.TempDir()
	got, err := SafeJoinUnderLibrary(root, "x/y")
	if err != nil {
		t.Fatalf("SafeJoinUnderLibrary() error = %v", err)
	}
	want := filepath.Join(root, "x", "y.echo")
	if got != want {
		t.Fatalf("SafeJoinUnderLibrary() = %q, want %q", got, want)
	}
}

func TestPrepareOutputUnderRootRejectsUnsafeFinal(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	outside := filepath.Join(tmp, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	dirFinal := filepath.Join(root, "x.echo")
	if err := os.Mkdir(dirFinal, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareOutputUnderRoot(root, "x", ".echo"); err == nil {
		t.Fatal("PrepareOutputUnderRoot(directory final) error = nil, want error")
	}

	target := filepath.Join(outside, "target.echo")
	link := filepath.Join(root, "link.echo")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := PrepareOutputUnderRoot(root, "link", ".echo"); err == nil {
		t.Fatal("PrepareOutputUnderRoot(symlink final) error = nil, want error")
	}
}

func TestHelpersPropagateFinalLstatErrors(t *testing.T) {
	root := t.TempDir()
	tooLongName := strings.Repeat("a", 300)

	if _, err := PrepareOutputUnderRoot(root, tooLongName, ".echo"); err == nil {
		t.Fatal("PrepareOutputUnderRoot(long final name) error = nil, want error")
	}
	if _, err := PrepareNewDirUnderRoot(root, tooLongName); err == nil {
		t.Fatal("PrepareNewDirUnderRoot(long final name) error = nil, want error")
	}
}

func TestPrepareOutputUnderRootRejectsBadSuffix(t *testing.T) {
	root := t.TempDir()
	tests := []string{"", "/x", `\x`, ".echo\n"}
	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			if _, err := PrepareOutputUnderRoot(root, "x", tt); err == nil {
				t.Fatal("PrepareOutputUnderRoot(bad suffix) error = nil, want error")
			}
		})
	}
}

func TestPrepareOutputUnderRootRejectsFileParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "file-parent")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareOutputUnderRoot(root, "file-parent/child", ".echo"); err == nil {
		t.Fatal("PrepareOutputUnderRoot(file parent) error = nil, want error")
	}
}

func TestPrepareOutputUnderRootRejectsSymlinkParentAndDoesNotCreateOutside(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	outside := filepath.Join(tmp, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	if _, err := PrepareOutputUnderRoot(root, "link/new/file", ".echo"); err == nil {
		t.Fatal("PrepareOutputUnderRoot(symlink parent) error = nil, want error")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside directory was touched, stat err = %v", err)
	}
}

func TestPrepareNewDirUnderRootCreatesFinalWithParents(t *testing.T) {
	root := t.TempDir()
	final, err := PrepareNewDirUnderRoot(root, "jobs/job-1")
	if err != nil {
		t.Fatalf("PrepareNewDirUnderRoot() error = %v", err)
	}
	want := filepath.Join(root, "jobs", "job-1")
	if final != want {
		t.Fatalf("PrepareNewDirUnderRoot() = %q, want %q", final, want)
	}
	info, err := os.Stat(final)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("final is not directory: mode=%v", info.Mode())
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("final dir mode = %#o, want 0750", got)
	}

	cleaned, err := PrepareNewDirUnderRoot(root, "./jobs//job-2")
	if err != nil {
		t.Fatalf("PrepareNewDirUnderRoot(clean rel) error = %v", err)
	}
	cleanWant := filepath.Join(root, "jobs", "job-2")
	if cleaned != cleanWant {
		t.Fatalf("PrepareNewDirUnderRoot(clean rel) = %q, want %q", cleaned, cleanWant)
	}
}

func TestPrepareNewDirUnderRootRejectsExistingFinal(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	outside := filepath.Join(tmp, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "job-1")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareNewDirUnderRoot(root, "job-1"); err == nil {
		t.Fatal("PrepareNewDirUnderRoot(existing) error = nil, want error")
	}

	existingFile := filepath.Join(root, "job-file")
	if err := os.WriteFile(existingFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareNewDirUnderRoot(root, "job-file"); err == nil {
		t.Fatal("PrepareNewDirUnderRoot(existing file) error = nil, want error")
	}

	if err := os.Symlink(outside, filepath.Join(root, "job-link")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := PrepareNewDirUnderRoot(root, "job-link"); err == nil {
		t.Fatal("PrepareNewDirUnderRoot(final symlink) error = nil, want error")
	}
}

func TestPrepareNewDirUnderRootRejectsSymlinkParentAndDoesNotCreateOutside(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	outside := filepath.Join(tmp, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	if _, err := PrepareNewDirUnderRoot(root, "link/job-1"); err == nil {
		t.Fatal("PrepareNewDirUnderRoot(symlink parent) error = nil, want error")
	}
	if _, err := os.Stat(filepath.Join(outside, "job-1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside directory was touched, stat err = %v", err)
	}
}

func TestPrepareNewDirUnderRootRejectsFileParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "file-parent")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareNewDirUnderRoot(root, "file-parent/job-1"); err == nil {
		t.Fatal("PrepareNewDirUnderRoot(file parent) error = nil, want error")
	}
}

func TestHelpersRejectParentTraversalEvenWhenCleanWouldStayUnderRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveExistingUnderRoot(root, "a/../b"); err == nil {
		t.Fatal("ResolveExistingUnderRoot traversal error = nil, want error")
	}
	if _, err := PrepareOutputUnderRoot(root, "a/../b", ".echo"); err == nil {
		t.Fatal("PrepareOutputUnderRoot traversal error = nil, want error")
	}
	if _, err := PrepareNewDirUnderRoot(root, "a/../b"); err == nil {
		t.Fatal("PrepareNewDirUnderRoot traversal error = nil, want error")
	}
}
