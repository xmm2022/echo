package sidecarclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/config"
	"github.com/xmm2022/echo/internal/sidecarclient/fakesidecar"
)

func TestVersionRequiresExactMinVersion(t *testing.T) {
	fake := fakesidecar.New(t, fakesidecar.Options{
		Version: "sidecar-abc123",
	})
	client := New(testConfig(fake.URL(), "sidecar-abc123"))

	got, err := client.Version(context.Background())
	if err != nil {
		t.Fatalf("Version returned error: %v", err)
	}
	if got != "sidecar-abc123" {
		t.Fatalf("Version = %q, want sidecar-abc123", got)
	}

	client = New(testConfig(fake.URL(), "sidecar-other"))
	_, err = client.Version(context.Background())
	if !errors.Is(err, ErrSidecarVersionTooOld) {
		t.Fatalf("Version error = %v, want ErrSidecarVersionTooOld", err)
	}
}

func TestRequestInjectsSidecarToken(t *testing.T) {
	t.Setenv("ECHO_TEST_SIDECAR_TOKEN", "secret-token")
	fake := fakesidecar.New(t, fakesidecar.Options{Version: "sidecar-abc123"})
	client := New(testConfig(fake.URL(), "sidecar-abc123"))

	if _, err := client.ListStorages(context.Background()); err != nil {
		t.Fatalf("ListStorages returned error: %v", err)
	}

	got := fake.LastAuthorization()
	if got != "secret-token" {
		t.Fatalf("Authorization = %q, want secret-token", got)
	}
}

func TestListStoragesAcceptsOpenListPageShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/admin/storage/list" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"content":[{"id":1,"provider":"wrong","driver":"GuangYaPan","mount_path":"/guangya","status":"work"}],"total":1}}`))
	}))
	t.Cleanup(server.Close)

	client := New(testConfig(server.URL, ""))
	storages, err := client.ListStorages(context.Background())
	if err != nil {
		t.Fatalf("ListStorages returned error: %v", err)
	}
	if len(storages) != 1 {
		t.Fatalf("len(storages) = %d, want 1", len(storages))
	}
	got := storages[0]
	if got.ID != "1" || got.Provider != "GuangYaPan" || got.MountPath != "/guangya" || got.Status != "work" {
		t.Fatalf("storage = %+v, want OpenList page storage decoded", got)
	}
}

func TestListStoragesRejectsNonPageShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":[{"id":1,"driver":"115","mount_path":"/115-main","status":"ok"}]}`))
	}))
	t.Cleanup(server.Close)

	client := New(testConfig(server.URL, ""))
	if _, err := client.ListStorages(context.Background()); err == nil {
		t.Fatal("ListStorages error = nil, want non-page shape rejected")
	}
}

func TestListStoragesRejectsStringID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"content":[{"id":"1","driver":"115","mount_path":"/115-main","status":"ok"}],"total":1}}`))
	}))
	t.Cleanup(server.Close)

	client := New(testConfig(server.URL, ""))
	if _, err := client.ListStorages(context.Background()); err == nil {
		t.Fatal("ListStorages error = nil, want string id rejected")
	}
}

func TestListStoragesRequiresOpenListEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"content":[{"id":1,"driver":"115","mount_path":"/115-main","status":"ok"}],"total":1}}`))
	}))
	t.Cleanup(server.Close)

	client := New(testConfig(server.URL, ""))
	if _, err := client.ListStorages(context.Background()); err == nil {
		t.Fatal("ListStorages error = nil, want missing OpenList code rejected")
	}
}

func TestUnreachableAndHTTPErrorAreSeparated(t *testing.T) {
	fake := fakesidecar.New(t, fakesidecar.Options{
		Version:       "sidecar-abc123",
		StorageStatus: http.StatusInternalServerError,
	})
	client := New(testConfig(fake.URL(), "sidecar-abc123"))

	_, err := client.ListStorages(context.Background())
	var httpErr *SidecarHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("ListStorages error = %T %[1]v, want SidecarHTTPError", err)
	}
	if httpErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("StatusCode = %d, want 500", httpErr.StatusCode)
	}

	unreachable := New(testConfig("http://127.0.0.1:1", "sidecar-abc123"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = unreachable.ListStorages(ctx)
	if !errors.Is(err, ErrSidecarUnreachable) {
		t.Fatalf("ListStorages error = %v, want ErrSidecarUnreachable", err)
	}
}

func TestRequestTimeoutMapsToUnreachable(t *testing.T) {
	fake := fakesidecar.New(t, fakesidecar.Options{
		Version: "sidecar-abc123",
		Hang:    true,
	})

	cfg := testConfig(fake.URL(), "sidecar-abc123")
	cfg.RequestTimeout = config.Duration{Duration: 10 * time.Millisecond}
	client := New(cfg)

	_, err := client.ListStorages(context.Background())
	if !errors.Is(err, ErrSidecarUnreachable) {
		t.Fatalf("ListStorages error = %v, want ErrSidecarUnreachable", err)
	}
}

func testConfig(baseURL, minVersion string) Config {
	return Config{
		BaseURL:        baseURL,
		AuthTokenEnv:   "ECHO_TEST_SIDECAR_TOKEN",
		MinVersion:     minVersion,
		RequestTimeout: config.Duration{Duration: 2 * time.Second},
		StreamTimeout:  config.Duration{Duration: 2 * time.Second},
	}
}

func TestMain(m *testing.M) {
	os.Unsetenv("ECHO_TEST_SIDECAR_TOKEN")
	os.Exit(m.Run())
}
