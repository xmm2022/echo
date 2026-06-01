package sidecarclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/sidecarclient/fakesidecar"
)

func TestStreamPassesWhitelistedHeadersAndReturnsPartialContent(t *testing.T) {
	fake := fakesidecar.New(t, fakesidecar.Options{Version: "sidecar-abc123"})
	client := New(testConfig(fake.URL(), "sidecar-abc123"))

	headers := http.Header{}
	headers.Set("Range", "bytes=0-3")
	headers.Set("If-Range", `"etag-1"`)
	headers.Set("If-Modified-Since", "Wed, 21 Oct 2015 07:28:00 GMT")
	headers.Set("If-None-Match", `"etag-1"`)
	headers.Set("User-Agent", "EchoTest/1.0")
	headers.Set("Cookie", "must-not-forward")

	result, err := client.Stream(context.Background(), StreamRequest{
		StorageMount: "/115-main",
		RemotePath:   "/Movies/Film.mkv",
		Headers:      headers,
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer result.Body.Close()

	if result.StatusCode != http.StatusPartialContent {
		t.Fatalf("StatusCode = %d, want 206", result.StatusCode)
	}
	if result.Header.Get("Content-Range") != "bytes 0-3/10" {
		t.Fatalf("Content-Range = %q", result.Header.Get("Content-Range"))
	}
	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "0123" {
		t.Fatalf("body = %q, want 0123", string(body))
	}

	req := fake.LastStreamRequest()
	if req.Header.Get("Range") != "bytes=0-3" || req.Header.Get("Cookie") != "" {
		t.Fatalf("unexpected forwarded headers: %#v", req.Header)
	}
}

func TestStreamHandlesNotModifiedAndRangeNotSatisfiable(t *testing.T) {
	tests := []struct {
		name      string
		headers   http.Header
		wantCode  int
		wantBody  bool
		wantRange string
	}{
		{
			name:     "304",
			headers:  http.Header{"If-None-Match": []string{`"stream-etag"`}},
			wantCode: http.StatusNotModified,
			wantBody: true,
		},
		{
			name:      "416",
			headers:   http.Header{"Range": []string{"bytes=100-200"}},
			wantCode:  http.StatusRequestedRangeNotSatisfiable,
			wantBody:  true,
			wantRange: "bytes */10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := fakesidecar.New(t, fakesidecar.Options{Version: "sidecar-abc123"})
			client := New(testConfig(fake.URL(), "sidecar-abc123"))

			result, err := client.Stream(context.Background(), StreamRequest{
				StorageMount: "/115-main",
				RemotePath:   "/Movies/Film.mkv",
				Headers:      tt.headers,
			})
			if err != nil {
				t.Fatalf("Stream returned error: %v", err)
			}
			if result.StatusCode != tt.wantCode {
				t.Fatalf("StatusCode = %d, want %d", result.StatusCode, tt.wantCode)
			}
			if tt.wantRange != "" && result.Header.Get("Content-Range") != tt.wantRange {
				t.Fatalf("Content-Range = %q, want %q", result.Header.Get("Content-Range"), tt.wantRange)
			}
			if tt.wantBody {
				result.Body.Close()
			} else if result.Body != nil {
				t.Fatalf("Body = %#v, want nil", result.Body)
			}
		})
	}
}

func TestStreamHTTP500ReturnsSidecarHTTPError(t *testing.T) {
	tests := []int{http.StatusNotFound, http.StatusGone, http.StatusInternalServerError}
	for _, status := range tests {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fake := fakesidecar.New(t, fakesidecar.Options{
				Version:      "sidecar-abc123",
				StreamStatus: status,
			})
			client := New(testConfig(fake.URL(), "sidecar-abc123"))

			_, err := client.Stream(context.Background(), StreamRequest{
				StorageMount: "/115-main",
				RemotePath:   "/Movies/Film.mkv",
			})
			var httpErr *SidecarHTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != status {
				t.Fatalf("Stream error = %T %[1]v, want SidecarHTTPError %d", err, status)
			}
		})
	}
}

func TestStreamClassifiesHTMLFailureAsSuspectTransient(t *testing.T) {
	const htmlBody = "<html><body>failed to get file: object not found</body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(htmlBody))
	}))
	defer srv.Close()

	// Signature adaptation (see plan note): the real Config uses AuthTokenEnv +
	// config.Duration, so the plan's literal New(Config{AuthToken:"tok", ...})
	// does not compile. Use the package's testConfig helper instead.
	client := New(testConfig(srv.URL, ""))
	_, err := client.Stream(context.Background(), StreamRequest{
		StorageMount: "/mnt",
		RemotePath:   "/movie.mkv",
		Headers:      http.Header{"Range": []string{"bytes=0-1023"}},
	})
	var typed *SidecarTypedError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %T %[1]v, want SidecarTypedError", err)
	}
	if typed.Kind != SidecarErrTransient {
		t.Fatalf("kind = %s, want transient for /d/ HTML 500", typed.Kind)
	}
	if typed.EvidenceClass != "html_snippet" {
		t.Fatalf("evidence = %q, want html_snippet", typed.EvidenceClass)
	}
	if typed.Confidence != "suspect" {
		t.Fatalf("confidence = %q, want suspect", typed.Confidence)
	}
	if typed.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("http status = %d, want 500", typed.HTTPStatus)
	}
	if strings.Contains(typed.SafeMessage, "<html>") {
		t.Fatalf("safe message leaked raw HTML: %q", typed.SafeMessage)
	}
}

func TestStreamFallsBackToSignedDirectLinkWhenDPathRequiresExpire(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/d/mnt/movie.mkv":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("401 Unauthorized expire missing"))
		case "/api/fs/link":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"url":"` + srv.URL + `/download/movie.mkv","headers":{"X-Download-Token":"signed"}}}`))
		case "/download/movie.mkv":
			if got := r.Header.Get("X-Download-Token"); got != "signed" {
				t.Fatalf("direct-link header = %q, want signed", got)
			}
			if got := r.Header.Get("Range"); got != "bytes=0-3" {
				t.Fatalf("range header = %q, want bytes=0-3", got)
			}
			w.Header().Set("Content-Range", "bytes 0-3/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("0123"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := New(testConfig(srv.URL, ""))
	result, err := client.Stream(context.Background(), StreamRequest{
		StorageMount: "/mnt",
		RemotePath:   "/movie.mkv",
		Headers:      http.Header{"Range": []string{"bytes=0-3"}},
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusPartialContent {
		t.Fatalf("StatusCode = %d, want 206", result.StatusCode)
	}
	if got := result.Header.Get("Content-Range"); got != "bytes 0-3/10" {
		t.Fatalf("Content-Range = %q, want bytes 0-3/10", got)
	}
	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "0123" {
		t.Fatalf("body = %q, want 0123", body)
	}
}

func TestStreamTimeoutReturnsUnreachable(t *testing.T) {
	fake := fakesidecar.New(t, fakesidecar.Options{
		Version: "sidecar-abc123",
		Hang:    true,
	})
	cfg := testConfig(fake.URL(), "sidecar-abc123")
	cfg.StreamTimeout.Duration = 10 * time.Millisecond
	client := New(cfg)

	_, err := client.Stream(context.Background(), StreamRequest{
		StorageMount: "/115-main",
		RemotePath:   "/Movies/Film.mkv",
	})
	if !errors.Is(err, ErrSidecarUnreachable) {
		t.Fatalf("Stream error = %v, want ErrSidecarUnreachable", err)
	}
}
