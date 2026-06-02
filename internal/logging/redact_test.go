package logging_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/xmm2022/echo/internal/logging"
)

func newLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(logging.NewRedactHandler(slog.NewJSONHandler(buf, nil)))
}

// captureRedactedLog renders attrs through the redaction handler into a buffer and
// returns the emitted log line as a string.
func captureRedactedLog(t *testing.T, attrs []slog.Attr) string {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(logging.NewRedactHandler(slog.NewJSONHandler(&buf, nil)))
	logger.LogAttrs(context.Background(), slog.LevelInfo, "event", attrs...)
	return buf.String()
}

func TestRedactsSensitiveKeys(t *testing.T) {
	keys := []string{"cookie", "token", "authorization", "sign", "signature", "password"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			var buf bytes.Buffer
			newLogger(&buf).Info("event", key, "supersecret", "user", "alice")
			out := buf.String()
			if strings.Contains(out, "supersecret") {
				t.Errorf("key %q leaked its value: %s", key, out)
			}
			if !strings.Contains(out, "<redacted>") {
				t.Errorf("key %q not redacted: %s", key, out)
			}
			if !strings.Contains(out, "alice") {
				t.Errorf("non-sensitive attr dropped: %s", out)
			}
		})
	}
}

func TestRedactIsCaseInsensitive(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Info("event", "Authorization", "Bearer xyz")
	if strings.Contains(buf.String(), "xyz") {
		t.Errorf("Authorization (mixed case) leaked: %s", buf.String())
	}
}

func TestRedactsSignedURLValueUnderAnyKey(t *testing.T) {
	cases := []string{
		"https://cdn.example.com/d/file.mkv?sign=abcSECRET123:0",
		"https://cdn.example.com/d/file.mkv?signature=abcSECRET123",
		"https://cdn.example.com/d/file.mkv?token=abcSECRET123",
		"https://cdn.example.com/d/file.mkv?a=1&sign=abcSECRET123",
	}
	for _, url := range cases {
		var buf bytes.Buffer
		newLogger(&buf).Info("link", "url", url)
		out := buf.String()
		if strings.Contains(out, "abcSECRET123") {
			t.Errorf("signed URL leaked: %s", out)
		}
		if !strings.Contains(out, "<redacted>") {
			t.Errorf("signed URL not redacted: %s", out)
		}
	}
}

func TestPlainURLNotRedacted(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Info("link", "url", "https://example.com/d/file.mkv")
	if !strings.Contains(buf.String(), "example.com/d/file.mkv") {
		t.Errorf("plain URL should pass through: %s", buf.String())
	}
}

func TestRedactsWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(logging.NewRedactHandler(slog.NewJSONHandler(&buf, nil)))
	logger.With("token", "t0psecret").Info("event")
	if strings.Contains(buf.String(), "t0psecret") {
		t.Errorf("With() attr leaked: %s", buf.String())
	}
}

func TestRedactsInlineGroup(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Info("event", slog.Group("creds", "password", "hunter2", "username", "bob"))
	out := buf.String()
	if strings.Contains(out, "hunter2") {
		t.Errorf("group password leaked: %s", out)
	}
	if !strings.Contains(out, "bob") {
		t.Errorf("group non-sensitive attr dropped: %s", out)
	}
}

func TestRedactsWithGroup(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(logging.NewRedactHandler(slog.NewJSONHandler(&buf, nil)))
	logger.WithGroup("auth").Info("event", "cookie", "yummy")
	if strings.Contains(buf.String(), "yummy") {
		t.Errorf("WithGroup attr leaked: %s", buf.String())
	}
}

func TestRedactsV02Secrets(t *testing.T) {
	got := captureRedactedLog(t, []slog.Attr{
		slog.String("playback_token", "sel.secret"),
		slog.String("selector", "sel"),
		slog.String("secret", "secret"),
		slog.String("api_key", "emby-key"),
		slog.String("x_emby_token", "emby-token"),
		slog.String("authorization", "MediaBrowser Token=emby-token"),
		slog.String("cookie", "EmbyAuth=abc"),
		slog.String("safe_reason", "quota_exceeded"),
	})
	for _, forbidden := range []string{"sel.secret", "emby-key", "emby-token", "EmbyAuth=abc"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("log leaked %q in %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "quota_exceeded") {
		t.Fatalf("safe enum missing from log: %s", got)
	}
}

func TestRedactDiscoverySecrets(t *testing.T) {
	got := captureRedactedLog(t, []slog.Attr{
		slog.String("receive_code", "abcd"),
		slog.String("api_hash", "api-hash-secret"),
		slog.String("tmdb_key", "tmdb-secret"),
		slog.String("session_ref", "ref:telegram/session.json"),
		slog.String("share_url", "https://115.com/s/abc?password=secret"),
	})
	for _, forbidden := range []string{"abcd", "api-hash-secret", "tmdb-secret", "ref:telegram/session.json", "password=secret"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("discovery secret leaked %q in %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("redaction marker missing: %s", got)
	}
}

func TestRedactionPreservesSensitiveKeyNames(t *testing.T) {
	got := captureRedactedLog(t, []slog.Attr{
		slog.String("api_hash", "api-hash-secret"),
	})
	if !strings.Contains(got, `"api_hash":"<redacted>"`) {
		t.Fatalf("sensitive key name was not preserved: %s", got)
	}
}

func TestRedactDiscoveryShareCodeAndSessionPath(t *testing.T) {
	got := captureRedactedLog(t, []slog.Attr{
		slog.String("share_code", "share-secret"),
		slog.String("session_path", "/var/lib/echo/telegram/session.json"),
	})
	for _, forbidden := range []string{"share-secret", "/var/lib/echo/telegram/session.json"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("discovery secret leaked %q in %s", forbidden, got)
		}
	}
}

func TestRedactsCamelCaseAPIKey(t *testing.T) {
	got := captureRedactedLog(t, []slog.Attr{
		slog.String("apiKey", "api-key-secret"),
	})
	if strings.Contains(got, "api-key-secret") {
		t.Fatalf("camelCase apiKey leaked in %s", got)
	}
}

func TestRedactsDiscoveryPayloadAndErrorStrings(t *testing.T) {
	got := captureRedactedLog(t, []slog.Attr{
		slog.String("payload", `{"receive_code":"abcd","api_hash":"telegramhash","session_path":"/var/lib/echo/telegram/session.json"}`),
		slog.Any("err", errors.New("receive_code=efgh session_path=/var/lib/echo/telegram/session.json")),
	})
	for _, forbidden := range []string{"abcd", "telegramhash", "efgh", "/var/lib/echo/telegram/session.json"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("discovery payload or error leaked %q in %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("redaction marker missing: %s", got)
	}
}
