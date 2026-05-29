package restore

import (
	"net/http"
	"testing"

	"github.com/xmm2022/echo/internal/store/queries"
)

func TestForwardHeadersKeepsAllowlistedHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("Range", "bytes=0-1023")
	src.Set("If-Range", `"etag"`)
	src.Set("If-Modified-Since", "Wed, 21 Oct 2015 07:28:00 GMT")
	src.Set("If-None-Match", `"etag"`)
	src.Set("User-Agent", "Emby/4.7")

	dst := ForwardHeaders(src)

	for key, want := range map[string]string{
		"Range":             "bytes=0-1023",
		"If-Range":          `"etag"`,
		"If-Modified-Since": "Wed, 21 Oct 2015 07:28:00 GMT",
		"If-None-Match":     `"etag"`,
		"User-Agent":        "Emby/4.7",
	} {
		if got := dst.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestForwardHeadersDropsNonAllowlisted(t *testing.T) {
	src := http.Header{}
	src.Set("Cookie", "session=secret")
	src.Set("Authorization", "Bearer admin-token")
	src.Set("X-Forwarded-For", "1.2.3.4")

	dst := ForwardHeaders(src)

	for _, key := range []string{"Cookie", "Authorization", "X-Forwarded-For"} {
		if got := dst.Get(key); got != "" {
			t.Fatalf("%s leaked = %q, want dropped", key, got)
		}
	}
}

func TestStreamRequestForUsesCopyMountAndPathAndAllowlist(t *testing.T) {
	fc := queries.FileCopy{
		StorageMount: "/115-main",
		RemotePath:   "/media/episode.mkv",
	}
	client := http.Header{}
	client.Set("Range", "bytes=10-20")
	client.Set("Cookie", "session=secret")

	req := StreamRequestFor(fc, client)

	if req.StorageMount != "/115-main" {
		t.Fatalf("StorageMount = %q, want /115-main", req.StorageMount)
	}
	if req.RemotePath != "/media/episode.mkv" {
		t.Fatalf("RemotePath = %q, want /media/episode.mkv", req.RemotePath)
	}
	if got := req.Headers.Get("Range"); got != "bytes=10-20" {
		t.Fatalf("forwarded Range = %q, want bytes=10-20", got)
	}
	if got := req.Headers.Get("Cookie"); got != "" {
		t.Fatalf("Cookie leaked into StreamRequest = %q, want dropped", got)
	}
}
