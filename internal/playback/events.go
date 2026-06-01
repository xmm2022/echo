package playback

import (
	"regexp"
	"strings"
)

// maxFailureMessageLen bounds how much of a sidecar failure message is persisted to
// copy_failures.safe_message / file_copies.last_failure_message and surfaced in logs.
// It keeps a single hostile or pathological message from bloating the row or a log line.
const maxFailureMessageLen = 512

// redactionMarker replaces a redacted secret value. It mirrors logging.redactedValue so
// scrubbed playback failure text reads the same as the slog backstop's output.
const redactionMarker = "<redacted>"

// secretKeyPattern enumerates the secret vocabulary shared with internal/logging/redact.go
// (cookie, token, authorization, sign, signature, password). The alternation lists the
// longer names first so the regexp engine prefers e.g. "signature" over "sign".
const secretKeyPattern = `(?:authorization|signature|password|cookie|token|sign)`

// secretAssignmentRE matches a secret-bearing assignment anywhere in a free-form string:
// an optional URL separator (`?`/`&`), a word boundary, one of the secret keys, `=`, and
// the value that follows. The `\b` keeps a secret key embedded in a larger word (e.g. the
// "sign" in "design"/"redesign") from matching — mirroring internal/logging/redact.go,
// which redacts "sign"/"token" only at a query separator and never inside "design". The
// value run deliberately stops at characters that delimit the next field (whitespace, and
// the URL separators & # ?) so only the secret value is replaced and any trailing context
// (further query params, words) is preserved. Matching is case-insensitive so SIGN=,
// Token=, etc. are caught too.
var secretAssignmentRE = regexp.MustCompile(`(?i)([?&]?\b` + secretKeyPattern + `=)[^\s&#?]*`)

// redactSignedFragments removes secret values from a free-form sidecar message. It covers
// both bare `key=value` fragments and signed direct-link query params (`?sign=…`,
// `&signature=…`, `?token=…`, …) using the secret vocabulary from
// internal/logging/redact.go. The key is preserved and the value is replaced with the
// redaction marker, so `token=secret` becomes `token=<redacted>` and
// `https://h/d/x?sign=ABC:0` becomes `https://h/d/x?sign=<redacted>`. RawMessage from a
// SidecarTypedError must never be passed to logs/clients directly; callers route through
// SafeFailureMessage, which applies this scrub first.
func redactSignedFragments(s string) string {
	return secretAssignmentRE.ReplaceAllString(s, "${1}"+redactionMarker)
}

// SafeFailureMessage normalizes a sidecar-provided failure message for persistence and
// logging: it trims surrounding whitespace, redacts secret-bearing fragments, and only
// then truncates to maxFailureMessageLen. Redaction happens BEFORE truncation so a secret
// near the end of an over-long message is scrubbed rather than relying on the length cap
// to drop it.
func SafeFailureMessage(raw string) string {
	s := strings.TrimSpace(raw)
	s = redactSignedFragments(s)
	if len(s) > maxFailureMessageLen {
		s = s[:maxFailureMessageLen]
	}
	return s
}
