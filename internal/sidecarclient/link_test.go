package sidecarclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestLinkClassifiesOpenListEnvelopeErrors(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		wantKind       SidecarErrorKind
		wantCode       int
		wantSafeMsg    string
		wantEvidence   string
		wantConfidence string
	}{
		{
			name:           "object missing",
			body:           `{"code":500,"message":"failed to get file: object not found","data":null}`,
			wantKind:       SidecarErrObjectMissing,
			wantCode:       500,
			wantSafeMsg:    "failed to get file: object not found",
			wantEvidence:   "json_envelope",
			wantConfidence: "confirmed",
		},
		{
			name:           "storage missing",
			body:           `{"code":500,"message":"storage not found: movies","data":null}`,
			wantKind:       SidecarErrStorageMissing,
			wantCode:       500,
			wantSafeMsg:    "storage not found: movies",
			wantEvidence:   "json_envelope",
			wantConfidence: "suspect",
		},
		{
			name:           "auth account",
			body:           `{"code":500,"message":"access_token is empty","data":null}`,
			wantKind:       SidecarErrAuthOrAccount,
			wantCode:       500,
			wantSafeMsg:    "access_token is empty",
			wantEvidence:   "json_envelope",
			wantConfidence: "suspect",
		},
		{
			name:           "transient envelope",
			body:           `{"code":500,"message":"upstream busy","data":null}`,
			wantKind:       SidecarErrTransient,
			wantCode:       500,
			wantSafeMsg:    "upstream busy",
			wantEvidence:   "json_envelope",
			wantConfidence: "low",
		},
		{
			name:           "code zero is not success",
			body:           `{"code":0,"message":"ambiguous sidecar response","data":null}`,
			wantKind:       SidecarErrProtocol,
			wantCode:       0,
			wantSafeMsg:    "ambiguous sidecar response",
			wantEvidence:   "json_envelope",
			wantConfidence: "low",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/fs/link" {
					t.Fatalf("path = %s, want /api/fs/link", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			client := New(testConfig(srv.URL, ""))
			_, err := client.Link(context.Background(), "/mnt", "/movie.mkv")
			var typed *SidecarTypedError
			if !errors.As(err, &typed) {
				t.Fatalf("error = %T %[1]v, want SidecarTypedError", err)
			}
			if typed.Kind != tt.wantKind {
				t.Fatalf("kind = %s, want %s", typed.Kind, tt.wantKind)
			}
			if typed.OpenListCode != tt.wantCode {
				t.Fatalf("openlist code = %d, want %d", typed.OpenListCode, tt.wantCode)
			}
			if typed.SafeMessage != tt.wantSafeMsg {
				t.Fatalf("safe message = %q, want %q", typed.SafeMessage, tt.wantSafeMsg)
			}
			if typed.EvidenceClass != tt.wantEvidence {
				t.Fatalf("evidence = %q, want %q", typed.EvidenceClass, tt.wantEvidence)
			}
			if typed.Confidence != tt.wantConfidence {
				t.Fatalf("confidence = %q, want %q", typed.Confidence, tt.wantConfidence)
			}
		})
	}
}
