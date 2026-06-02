package discovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	storeq "github.com/xmm2022/echo/internal/store/queries"
)

func TestRawStoreWritesRedactedCappedPayload(t *testing.T) {
	store := NewRawStore(RawStoreConfig{Root: t.TempDir(), MaxBytes: 16})
	ref, redacted, err := store.Put(context.Background(), "source-1", "tg:10", []byte("password=secret receive_code=abcd long payload"))
	if err != nil {
		t.Fatal(err)
	}
	if ref == "" || !strings.HasPrefix(ref, "raw:") {
		t.Fatalf("ref = %q", ref)
	}
	if strings.Contains(redacted, "secret") || strings.Contains(redacted, "abcd") {
		t.Fatalf("redacted leaked secret: %q", redacted)
	}
	body, err := store.Get(context.Background(), ref, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 8 {
		t.Fatalf("body len = %d, want <= 8", len(body))
	}
}

func TestRawStoreRejectsRefEscape(t *testing.T) {
	store := NewRawStore(RawStoreConfig{Root: t.TempDir(), MaxBytes: 1024})
	_, err := store.Get(context.Background(), "raw:../escape", 1024)
	if err == nil {
		t.Fatal("expected escape rejection")
	}
}

func TestParsedJSONContractRequiresRedactedData(t *testing.T) {
	if err := ValidateParsedJSONForStorage([]byte(`{"title":"safe","raw_text_ref":"raw:abc"}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateParsedJSONForStorage([]byte(`{"receive_code":"abcd"}`)); err == nil {
		t.Fatal("expected parsed_json sensitive field rejection")
	}
}

func TestRawStoreRedactsDiscoveryCredentialPreview(t *testing.T) {
	store := NewRawStore(RawStoreConfig{Root: t.TempDir(), MaxBytes: 1024})
	_, redacted, err := store.Put(context.Background(), "source-1", "tg:11", []byte("api_hash=hash api_key=api-secret tmdb_key=tmdb-secret"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"hash", "api-secret", "tmdb-secret"} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("redacted leaked credential %q in %q", forbidden, redacted)
		}
	}
}

func TestRawStoreRedactsJSONCredentialPreview(t *testing.T) {
	store := NewRawStore(RawStoreConfig{Root: t.TempDir(), MaxBytes: 1024})
	_, redacted, err := store.Put(context.Background(), "source-1", "tg:12", []byte(`{"api_hash":"hash-json","api_key":"api-json","tmdb_key":"tmdb-json"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"hash-json", "api-json", "tmdb-json"} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("redacted leaked JSON credential %q in %q", forbidden, redacted)
		}
	}
}

func TestRawStoreRedactsComplexJSONSecretValues(t *testing.T) {
	store := NewRawStore(RawStoreConfig{Root: t.TempDir(), MaxBytes: 2048})
	raw := []byte(`{"password":"top secret","nested":{"api_key":"comma,value","tmdb_key":"quote \" inside","items":[{"receive_code":"line1\nline2"}]}}`)
	_, redacted, err := store.Put(context.Background(), "source-1", "tg:13", raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"top", "secret", "comma,value", "quote", "inside", "line1", "line2"} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("redacted leaked complex JSON secret %q in %q", forbidden, redacted)
		}
	}
}

func TestRawStoreRedactsFallbackTextSecretValues(t *testing.T) {
	store := NewRawStore(RawStoreConfig{Root: t.TempDir(), MaxBytes: 2048})
	raw := []byte(`password="top secret" api_key="escaped \" quote" tmdb_key=token, receive_code='multi
line' safe=value`)
	_, redacted, err := store.Put(context.Background(), "source-1", "tg:14", raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"top secret", `escaped \" quote`, "token", "multi", "line"} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("redacted leaked fallback secret %q in %q", forbidden, redacted)
		}
	}
	if !strings.Contains(redacted, "safe=value") {
		t.Fatalf("non-secret fallback text was dropped: %q", redacted)
	}
}

func TestRawStorePutRejectsZeroMaxBytes(t *testing.T) {
	store := NewRawStore(RawStoreConfig{Root: t.TempDir()})
	if _, _, err := store.Put(context.Background(), "source-1", "tg:15", []byte("payload")); err == nil {
		t.Fatal("expected zero MaxBytes rejection")
	}
}

func TestRawStoreRejectsSymlinkLeafOnGet(t *testing.T) {
	root := t.TempDir()
	store := NewRawStore(RawStoreConfig{Root: root, MaxBytes: 1024})
	ref, _, err := store.Put(context.Background(), "source-1", "tg:16", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	path := rawTestPath(root, "payload")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.Get(context.Background(), ref, 1024); err == nil {
		t.Fatal("expected symlink leaf rejection")
	}
}

func TestRawStoreRejectsSymlinkLeafOnPut(t *testing.T) {
	root := t.TempDir()
	store := NewRawStore(RawStoreConfig{Root: root, MaxBytes: 1024})
	payload := []byte("payload")
	path := rawTestPath(root, string(payload))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := store.Put(context.Background(), "source-1", "tg:17", payload); err == nil {
		t.Fatal("expected symlink leaf rejection")
	}
}

func TestRawStoreRejectsSymlinkPrefixDirOnPut(t *testing.T) {
	root := t.TempDir()
	store := NewRawStore(RawStoreConfig{Root: root, MaxBytes: 1024})
	payload := []byte("payload")
	prefixDir := filepath.Dir(rawTestPath(root, string(payload)))
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, prefixDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := store.Put(context.Background(), "source-1", "tg:18", payload); err == nil {
		t.Fatal("expected symlink prefix directory rejection")
	}
}

func TestRawStoreRejectsSymlinkPrefixDirOnGet(t *testing.T) {
	root := t.TempDir()
	store := NewRawStore(RawStoreConfig{Root: root, MaxBytes: 1024})
	payload := []byte("payload")
	ref, _, err := store.Put(context.Background(), "source-1", "tg:19", payload)
	if err != nil {
		t.Fatal(err)
	}
	path := rawTestPath(root, string(payload))
	prefixDir := filepath.Dir(path)
	if err := os.RemoveAll(prefixDir); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, filepath.Base(path)), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, prefixDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.Get(context.Background(), ref, 1024); err == nil {
		t.Fatal("expected symlink prefix directory rejection")
	}
}

func TestRawStoreEnsurePrefixDirAllowsConcurrentFirstCreate(t *testing.T) {
	root := t.TempDir()
	path := rawTestPath(root, "payload")
	const workers = 64
	start := make(chan struct{})
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for range workers {
		go func() {
			ready.Done()
			<-start
			errs <- ensureRawPrefixDir(path)
		}()
	}
	ready.Wait()
	close(start)
	for range workers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent prefix create failed: %v", err)
		}
	}
}

func TestRawStorePrefixDirCreateTreatsErrExistAsRetryable(t *testing.T) {
	prefixDir := filepath.Join(t.TempDir(), "aa")
	if err := os.Mkdir(prefixDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := finishRawPrefixDirCreate(prefixDir, os.ErrExist); err != nil {
		t.Fatalf("ErrExist should validate existing prefix dir: %v", err)
	}
}

func TestRawStorePruneDoesNotRemoveSymlinkLeaf(t *testing.T) {
	root := t.TempDir()
	store := NewRawStore(RawStoreConfig{Root: root, MaxBytes: 1024})
	dir := filepath.Join(root, "aa")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, strings.Repeat("a", 64))
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := store.Prune(context.Background(), 1<<62); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink leaf should remain after prune: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("symlink target should remain after prune: %v", err)
	}
}

func TestParsedJSONRejectsSensitiveStringValues(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"note":"receive_code=abcd"}`),
		[]byte(`{"url":"https://x.example/path?password=secret"}`),
		[]byte(`{"nested":["/var/lib/echo/telegram/session.json"]}`),
	}
	for _, data := range cases {
		if err := ValidateParsedJSONForStorage(data); err == nil {
			t.Fatalf("expected parsed_json sensitive string rejection for %s", data)
		}
	}
}

func TestSafeDiscoveredResourceWriterValidatesParsedJSONBeforeWrite(t *testing.T) {
	inner := &fakeDiscoveredResourceWriter{}
	writer := NewSafeDiscoveredResourceWriter(inner)
	_, err := writer.UpsertDiscoveredResource(context.Background(), storeq.UpsertDiscoveredResourceParams{
		ParsedJson:  `{"receive_code":"abcd"}`,
		FeatureJson: "{}",
	})
	if err == nil {
		t.Fatal("expected parsed_json validation error")
	}
	if inner.called {
		t.Fatal("unsafe parsed_json was written")
	}
}

func TestSafeDiscoveredResourceWriterDelegatesRedactedParsedJSON(t *testing.T) {
	inner := &fakeDiscoveredResourceWriter{}
	writer := NewSafeDiscoveredResourceWriter(inner)
	_, err := writer.UpsertDiscoveredResource(context.Background(), storeq.UpsertDiscoveredResourceParams{
		ParsedJson:      `{"title":"safe","raw_text_ref":"raw:abc"}`,
		FeatureJson:     "{}",
		RawTextRedacted: sql.NullString{String: "<redacted>", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !inner.called {
		t.Fatal("expected safe parsed_json write")
	}
}

func TestDiscoveredResourceStoreUsesSafeParsedJSONWriter(t *testing.T) {
	inner := &fakeDiscoveredResourceWriter{}
	store := NewDiscoveredResourceStore(inner)
	_, err := store.UpsertDiscoveredResource(context.Background(), storeq.UpsertDiscoveredResourceParams{
		ParsedJson:  `{"note":"receive_code=abcd"}`,
		FeatureJson: "{}",
	})
	if err == nil {
		t.Fatal("expected parsed_json validation error")
	}
	if inner.called {
		t.Fatal("unsafe parsed_json was written through store facade")
	}
}

type fakeDiscoveredResourceWriter struct {
	called bool
}

func (f *fakeDiscoveredResourceWriter) UpsertDiscoveredResource(ctx context.Context, arg storeq.UpsertDiscoveredResourceParams) (storeq.DiscoveredResource, error) {
	f.called = true
	return storeq.DiscoveredResource{}, nil
}

func rawTestPath(root, payload string) string {
	sum := sha256.Sum256([]byte(payload))
	hexSum := hex.EncodeToString(sum[:])
	return filepath.Join(root, hexSum[:2], hexSum)
}
