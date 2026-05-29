package logging_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/xmm2022/echo/internal/logging"
)

func newLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(logging.NewRedactHandler(slog.NewJSONHandler(buf, nil)))
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
