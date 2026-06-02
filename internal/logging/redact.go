// Package logging provides a slog.Handler middleware that redacts secrets from
// log output. It is Echo's backstop (spec §8): even when a call site forgets to
// scrub a value, sensitive attribute keys and signed direct-link URLs never reach
// stdout. Structured scrubbing of producer argv still happens at the source
// (ingest.RedactProducerArgv); this handler catches the rest.
package logging

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
)

const redactedValue = "<redacted>"

// sensitiveKeys is the exact (case-insensitive) set of attribute keys whose
// values are always replaced (spec §8). v0.2 adds the Emby reverse-proxy /
// playback-token secret keys (api_key, x_emby_token, playback_token, selector,
// secret) on top of the v0.1 set.
var sensitiveKeys = map[string]struct{}{
	"cookie":         {},
	"token":          {},
	"authorization":  {},
	"sign":           {},
	"signature":      {},
	"password":       {},
	"api_key":        {},
	"apikey":         {},
	"api_hash":       {},
	"apihash":        {},
	"receive_code":   {},
	"receivecode":    {},
	"share_code":     {},
	"sharecode":      {},
	"share_password": {},
	"sharepassword":  {},
	"tmdb_key":       {},
	"tmdbkey":        {},
	"session_ref":    {},
	"sessionref":     {},
	"session_path":   {},
	"sessionpath":    {},
	"x_emby_token":   {},
	"playback_token": {},
	"selector":       {},
	"secret":         {},
}

// signedParamMarkers flag a URL carrying a signed query parameter. Both the
// leading ("?") and subsequent ("&") query separators are covered. The
// X-Emby-Token marker is stored lowercase because containsSignedParam lowercases
// the value before matching.
var signedParamMarkers = []string{
	"?sign=", "&sign=",
	"?signature=", "&signature=",
	"?token=", "&token=",
	"?api_key=", "&api_key=",
	"?password=", "&password=",
	"?pwd=", "&pwd=",
	"?receive_code=", "&receive_code=",
	"?receivecode=", "&receivecode=",
	"x-emby-token=",
}

var sensitiveStringPattern = regexp.MustCompile(`(?i)"?(password|pwd|receive_code|receiveCode|share_code|shareCode|share_password|sharePassword|api_hash|apiHash|api_key|apiKey|tmdb_key|tmdbKey|session_ref|sessionRef|session_path|sessionPath)"?\s*[:=]`)

// RedactHandler wraps another slog.Handler, redacting sensitive attributes before
// they are emitted.
type RedactHandler struct {
	inner slog.Handler
}

// NewRedactHandler wraps inner with redaction.
func NewRedactHandler(inner slog.Handler) *RedactHandler {
	return &RedactHandler{inner: inner}
}

func (h *RedactHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *RedactHandler) Handle(ctx context.Context, r slog.Record) error {
	clone := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clone.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clone)
}

func (h *RedactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redactAttr(a)
	}
	return &RedactHandler{inner: h.inner.WithAttrs(redacted)}
}

func (h *RedactHandler) WithGroup(name string) slog.Handler {
	return &RedactHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr resolves LogValuers, recurses into groups, and replaces any
// sensitive key or signed-URL value with the redaction marker.
func redactAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()

	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		redacted := make([]slog.Attr, len(group))
		for i, g := range group {
			redacted[i] = redactAttr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(redacted...)}
	}

	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, redactedValue)
	}
	if shouldRedactValue(a.Value) {
		return slog.String(a.Key, redactedValue)
	}
	return a
}

func isSensitiveKey(key string) bool {
	_, ok := sensitiveKeys[strings.ToLower(key)]
	return ok
}

func containsSignedParam(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range signedParamMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func shouldRedactValue(value slog.Value) bool {
	if value.Kind() == slog.KindString {
		return containsSensitiveString(value.String())
	}
	if value.Kind() == slog.KindAny {
		if err, ok := value.Any().(error); ok {
			return containsSensitiveString(err.Error())
		}
	}
	return false
}

func containsSensitiveString(value string) bool {
	lower := strings.ToLower(value)
	return containsSignedParam(value) ||
		sensitiveStringPattern.MatchString(value) ||
		strings.Contains(lower, "telegram/session") ||
		strings.Contains(lower, "session.json")
}
