package sidecarclient

import (
	"net/http"
	"regexp"
	"strings"
)

// classifySidecarMessage maps an OpenList JSON-envelope failure (or a 401/403
// HTTP status) to a typed sidecar error. The real sidecar reports admin/fs API
// failures as HTTP 200 with an envelope whose code != 200, so the operation +
// HTTP status + envelope code together decide how confident we are that the
// classification reflects a real object/storage/account fault.
func classifySidecarMessage(operation string, httpStatus, openListCode int, msg string) *SidecarTypedError {
	safe := safeSidecarMessage(msg)
	lower := strings.ToLower(safe)
	kind := SidecarErrTransient
	confidence := "low"
	evidence := "json_envelope"
	switch {
	case strings.Contains(lower, "object not found") || strings.Contains(lower, "failed to get file"):
		kind = SidecarErrObjectMissing
		if operation == "link" && httpStatus == http.StatusOK && openListCode != 200 && openListCode != 0 {
			confidence = "confirmed"
		} else {
			confidence = "suspect"
		}
	case strings.Contains(lower, "storage not found") || strings.Contains(lower, "please add a storage first"):
		kind = SidecarErrStorageMissing
		confidence = "suspect"
	case strings.Contains(lower, "access_token is empty") || strings.Contains(lower, "refresh token failed") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "forbidden"):
		kind = SidecarErrAuthOrAccount
		confidence = "suspect"
	case httpStatus == http.StatusUnauthorized || httpStatus == http.StatusForbidden:
		kind = SidecarErrAuthOrAccount
		evidence = "http_status"
		confidence = "suspect"
	case openListCode == 0:
		kind = SidecarErrProtocol
		confidence = "low"
	}
	return &SidecarTypedError{
		Kind:          kind,
		Operation:     operation,
		HTTPStatus:    httpStatus,
		OpenListCode:  openListCode,
		SafeMessage:   safe,
		RawMessage:    msg,
		EvidenceClass: evidence,
		Confidence:    confidence,
	}
}

var (
	htmlTagPattern   = regexp.MustCompile(`<[^>]*>`)
	secretKVPattern  = regexp.MustCompile(`(?i)\b(token|sign|signature)\s*=\s*[^\s&,;?"']*`)
	secretHdrPattern = regexp.MustCompile(`(?i)\b(authorization|cookie)\b\s*[:=]?\s*[^\r\n]*`)
)

// safeSidecarMessage produces a log-safe version of a sidecar error message: it
// strips raw HTML tags (so HTML snippets from /d/ failures do not bloat logs),
// removes obvious credential fragments (token=, sign=, signature=, Authorization,
// Cookie), trims whitespace, and caps the result at 512 bytes. It deliberately
// does NOT depend on internal/logging, whose redactor is a slog handler rather
// than a string API.
func safeSidecarMessage(msg string) string {
	out := htmlTagPattern.ReplaceAllString(msg, " ")
	out = secretKVPattern.ReplaceAllString(out, "[redacted]")
	out = secretHdrPattern.ReplaceAllString(out, "[redacted]")
	out = strings.Join(strings.Fields(out), " ")
	out = strings.TrimSpace(out)
	if len(out) > 512 {
		out = out[:512]
	}
	return out
}
