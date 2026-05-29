package sidecarclient

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/sidecarclient/fakesidecar"
)

func TestLinkReturnsDirectLink(t *testing.T) {
	fake := fakesidecar.New(t, fakesidecar.Options{Version: "sidecar-abc123"})
	client := New(testConfig(fake.URL(), "sidecar-abc123"))

	got, err := client.Link(context.Background(), "/115-main", "/Movies/Film.mkv")
	if err != nil {
		t.Fatalf("Link returned error: %v", err)
	}
	if got.URL != "https://download.example/Movies/Film.mkv" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.Headers.Get("X-Download-Token") != "fake-link-token" {
		t.Fatalf("headers = %#v", got.Headers)
	}
	if got.ExpiresAt.IsZero() || time.Until(got.ExpiresAt) <= 0 {
		t.Fatalf("ExpiresAt = %v, want future time", got.ExpiresAt)
	}

	req := fake.LastLinkRequest()
	if req.StorageMount != "/115-main" || req.RemotePath != "/Movies/Film.mkv" {
		t.Fatalf("unexpected link request: %#v", req)
	}
	if req.FullPath != "/115-main/Movies/Film.mkv" {
		t.Fatalf("link path = %q, want /115-main/Movies/Film.mkv", req.FullPath)
	}
}

func TestLinkHTTP404ReturnsSidecarHTTPError(t *testing.T) {
	tests := []int{http.StatusNotFound, http.StatusGone, http.StatusBadGateway}
	for _, status := range tests {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fake := fakesidecar.New(t, fakesidecar.Options{
				Version:    "sidecar-abc123",
				LinkStatus: status,
			})
			client := New(testConfig(fake.URL(), "sidecar-abc123"))

			_, err := client.Link(context.Background(), "/115-main", "/missing.mkv")
			var httpErr *SidecarHTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != status {
				t.Fatalf("Link error = %T %[1]v, want SidecarHTTPError %d", err, status)
			}
		})
	}
}

func TestLinkTimeoutReturnsUnreachable(t *testing.T) {
	fake := fakesidecar.New(t, fakesidecar.Options{
		Version: "sidecar-abc123",
		Hang:    true,
	})
	cfg := testConfig(fake.URL(), "sidecar-abc123")
	cfg.RequestTimeout.Duration = 10 * time.Millisecond
	client := New(cfg)

	_, err := client.Link(context.Background(), "/115-main", "/Movies/Film.mkv")
	if !errors.Is(err, ErrSidecarUnreachable) {
		t.Fatalf("Link error = %v, want ErrSidecarUnreachable", err)
	}
}
