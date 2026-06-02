package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRefRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	_, err := Resolve(root, "ref:../outside")
	if err == nil {
		t.Fatal("expected escape rejection")
	}
}

func TestResolveRefRejectsAbsoluteAndEmptyRefs(t *testing.T) {
	root := t.TempDir()
	for _, ref := range []string{"ref:", "ref:/absolute"} {
		if _, err := Resolve(root, ref); err == nil {
			t.Fatalf("expected rejection for %q", ref)
		}
	}
}

func TestResolveRefAcceptsRegularFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tmdb.key")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(root, "ref:tmdb.key")
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("resolved path = %q, want %q", got, path)
	}
}

func TestResolveEnv(t *testing.T) {
	t.Setenv("ECHO_TEST_SECRET", "value")
	got, err := ResolveEnv("env:ECHO_TEST_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if got != "value" {
		t.Fatalf("env value = %q", got)
	}
}

func TestResolveEnvRejectsAbsentVariable(t *testing.T) {
	t.Setenv("ECHO_TEST_SECRET_ABSENT", "")
	os.Unsetenv("ECHO_TEST_SECRET_ABSENT")
	if _, err := ResolveEnv("env:ECHO_TEST_SECRET_ABSENT"); err == nil {
		t.Fatal("expected absent env rejection")
	}
}

func TestResolveRefRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.key")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape.key")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Resolve(root, "ref:escape.key"); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}

func TestResolveRefRejectsNonRegularFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir.secret"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, "ref:dir.secret"); err == nil {
		t.Fatal("expected non-regular file rejection")
	}
}

func TestValidateRefAcceptsNonexistentFileRef(t *testing.T) {
	if err := ValidateRef(t.TempDir(), "ref:missing/secret.key"); err != nil {
		t.Fatalf("expected syntactically valid ref: %v", err)
	}
}

func TestRedactRef(t *testing.T) {
	if got := RedactRef("ref:telegram/session.json"); got != "[REDACTED]" {
		t.Fatalf("redacted ref = %q", got)
	}
}
