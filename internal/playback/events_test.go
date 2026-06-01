package playback

import (
	"strings"
	"testing"
)

func TestEventsRedactAndLimitFailureMessage(t *testing.T) {
	msg := strings.Repeat("x", 600) + " token=secret"
	got := SafeFailureMessage(msg)
	if len(got) > 512 {
		t.Fatalf("safe message length = %d, want <= 512", len(got))
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("safe message leaked secret: %q", got)
	}
}

// TestSafeFailureMessageRedactsWithoutTruncating proves the redaction step is doing
// real work rather than the 512-cap masking the secret. Both messages are well under
// 512 bytes, so any leaked secret would survive truncation and fail the assertion.
func TestSafeFailureMessageRedactsWithoutTruncating(t *testing.T) {
	t.Run("signed direct link", func(t *testing.T) {
		raw := "link failed: https://host/d/movie.mkv?sign=ABCDEF123456:0 expired"
		got := SafeFailureMessage(raw)
		if len(got) >= 512 {
			t.Fatalf("message unexpectedly long (%d); cap, not redaction, would mask the secret", len(got))
		}
		if strings.Contains(got, "ABCDEF123456") {
			t.Fatalf("signed-URL token leaked: %q", got)
		}
	})

	t.Run("bare token kv", func(t *testing.T) {
		raw := "auth rejected token=hunter2 for account acct1"
		got := SafeFailureMessage(raw)
		if len(got) >= 512 {
			t.Fatalf("message unexpectedly long (%d); cap, not redaction, would mask the secret", len(got))
		}
		if strings.Contains(got, "hunter2") {
			t.Fatalf("token value leaked: %q", got)
		}
	})
}

// TestSafeFailureMessageDoesNotOverRedact guards against the redaction regex matching a
// secret key embedded in a larger word (e.g. "design" contains "sign"). Only a key at a
// word boundary is a real secret; diagnostic text and path segments must survive intact.
func TestSafeFailureMessageDoesNotOverRedact(t *testing.T) {
	for _, raw := range []string{
		"rolling out redesign=v2 to users",
		"layout design=modern failed to load",
		"path /api/cosign=1/step rejected",
	} {
		if got := SafeFailureMessage(raw); got != raw {
			t.Errorf("over-redacted %q -> %q", raw, got)
		}
	}
	// Sanity: a genuine boundary-anchored secret is still redacted (the \b did not disable it).
	if got := SafeFailureMessage("url ?sign=DEADBEEF done"); strings.Contains(got, "DEADBEEF") {
		t.Errorf("boundary secret leaked: %q", got)
	}
}
