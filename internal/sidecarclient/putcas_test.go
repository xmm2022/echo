package sidecarclient

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/sidecarclient/fakesidecar"
)

func TestPutCASRestoresWithContentLengthAndFilePath(t *testing.T) {
	fake := fakesidecar.New(t, fakesidecar.Options{Version: "sidecar-abc123"})
	client := New(testConfig(fake.URL(), "sidecar-abc123"))

	result, err := client.PutCAS(context.Background(), PutCASRequest{
		StorageMount: "/115-main",
		RemoteDir:    "/Movies",
		CASName:      "Film.mkv.cas",
		CASBody:      bytes.NewReader([]byte("cas-payload")),
		CASSize:      int64(len("cas-payload")),
	})
	if err != nil {
		t.Fatalf("PutCAS returned error: %v", err)
	}
	if result.Status != StatusRestored {
		t.Fatalf("Status = %q, want %q", result.Status, StatusRestored)
	}
	if result.CloudPath != "/Movies/Film.mkv" {
		t.Fatalf("CloudPath = %q, want /Movies/Film.mkv", result.CloudPath)
	}

	req := fake.LastPutCASRequest()
	if req.FilePath != "/Movies/Film.mkv.cas" {
		t.Fatalf("File-Path = %q, want /Movies/Film.mkv.cas", req.FilePath)
	}
	if req.ContentLength != int64(len("cas-payload")) {
		t.Fatalf("ContentLength = %d, want %d", req.ContentLength, len("cas-payload"))
	}
}

func TestPutCASRejectsNonCASNameBeforeHTTP(t *testing.T) {
	fake := fakesidecar.New(t, fakesidecar.Options{Version: "sidecar-abc123"})
	client := New(testConfig(fake.URL(), "sidecar-abc123"))

	_, err := client.PutCAS(context.Background(), PutCASRequest{
		StorageMount: "/115-main",
		RemoteDir:    "/Movies",
		CASName:      "Film.mkv",
		CASBody:      bytes.NewReader([]byte("cas-payload")),
		CASSize:      int64(len("cas-payload")),
	})
	if !errors.Is(err, ErrCASRestoreFailed) {
		t.Fatalf("PutCAS error = %v, want ErrCASRestoreFailed", err)
	}
	if got := fake.PutCASCount(); got != 0 {
		t.Fatalf("fake sidecar saw %d PutCAS calls, want 0", got)
	}
}

func TestPutCASErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    error
		wantHTTP   bool
	}{
		{name: "4xx maps to restore failed", statusCode: http.StatusBadRequest, wantErr: ErrCASRestoreFailed},
		{name: "5xx maps to HTTP error", statusCode: http.StatusBadGateway, wantHTTP: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := fakesidecar.New(t, fakesidecar.Options{
				Version:      "sidecar-abc123",
				PutCASStatus: tt.statusCode,
			})
			client := New(testConfig(fake.URL(), "sidecar-abc123"))

			_, err := client.PutCAS(context.Background(), PutCASRequest{
				StorageMount: "/115-main",
				RemoteDir:    "/Movies",
				CASName:      "Film.mkv.cas",
				CASBody:      bytes.NewReader([]byte("cas-payload")),
				CASSize:      int64(len("cas-payload")),
			})
			if tt.wantHTTP {
				var httpErr *SidecarHTTPError
				if !errors.As(err, &httpErr) || httpErr.StatusCode != tt.statusCode {
					t.Fatalf("PutCAS error = %T %[1]v, want SidecarHTTPError status %d", err, tt.statusCode)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("PutCAS error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestPutCASTimeoutReturnsUnreachable(t *testing.T) {
	fake := fakesidecar.New(t, fakesidecar.Options{
		Version: "sidecar-abc123",
		Hang:    true,
	})
	cfg := testConfig(fake.URL(), "sidecar-abc123")
	cfg.RequestTimeout.Duration = 10 * time.Millisecond
	client := New(cfg)

	_, err := client.PutCAS(context.Background(), PutCASRequest{
		StorageMount: "/115-main",
		RemoteDir:    "/Movies",
		CASName:      "Film.mkv.cas",
		CASBody:      bytes.NewReader([]byte("cas-payload")),
		CASSize:      int64(len("cas-payload")),
	})
	if !errors.Is(err, ErrSidecarUnreachable) {
		t.Fatalf("PutCAS error = %v, want ErrSidecarUnreachable", err)
	}
}
