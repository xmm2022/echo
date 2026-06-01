package embyproxy

import (
	"errors"
	"net/url"
	"strings"
)

// PathNormVersion identifies the normalization algorithm used by NormalizeEmbyPath.
// It is stamped into MappingResult.PathNorm consumers / persisted rows so that a future
// change to the normalization rules can be detected and migrated rather than silently
// reinterpreting previously-stored paths.
const PathNormVersion = 1

// MappingRule is a single Emby-path → Echo-relative-path mapping. PrefixNorm is an
// already-normalized Emby path prefix (matched at segment boundaries); EchoRelPrefix is
// the relative location under the Echo resource root that the matched suffix is appended
// to. CaseSensitive selects whether the prefix match is byte-exact or case-folded.
type MappingRule struct {
	ID            int64
	LibraryID     int64
	PrefixNorm    string
	EchoRelPrefix string
	CaseSensitive bool
}

// MappingResult is the outcome of a successful MatchPath: the rule that matched, the
// derived Echo-relative path, and the normalized Emby path that produced the match.
type MappingResult struct {
	MappingID int64
	LibraryID int64
	RelPath   string
	PathNorm  string
}

// ErrNoMapping is returned by MatchPath when no rule's prefix matches the normalized path.
var ErrNoMapping = errors.New("no emby path mapping matched")

// NormalizeEmbyPath validates and canonicalizes a raw Emby media path into the single
// form Echo will match and serve. It is a security boundary: every accepted return value
// must be safe to treat as an absolute, traversal-free media path.
//
// The contract (all checks are rejections that return a non-nil error):
//   - empty input is rejected;
//   - a raw, percent-encoded slash (%2f/%2F) or backslash (%5c/%5C) is rejected BEFORE
//     decoding, so an attacker cannot smuggle a separator past the segment checks. The
//     scan is deliberately on the raw string: %252f does not contain the substring %2f,
//     so a doubly-encoded sequence survives as literal filename text;
//   - the input is percent-decoded EXACTLY ONCE (a malformed escape is rejected);
//   - backslashes are folded to forward slashes;
//   - any control rune (< 0x20, including NUL) is rejected;
//   - Unicode slash look-alikes (U+2215, U+2044, U+FF0F) are rejected so they cannot
//     impersonate a path separator after the fact;
//   - any ".." path segment is rejected;
//   - a Windows drive prefix ("X:") is rejected;
//   - a UNC / double-leading-slash ("//") path is rejected.
//
// Ordinary spaces and non-separator Unicode filename characters are preserved.
func NormalizeEmbyPath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty emby path")
	}

	// Scan the RAW (pre-decode) string for encoded separators. Lowercasing only touches
	// ASCII here and cannot synthesize a new %2f/%5c substring (e.g. %252F -> %252f still
	// has no %2f), so it is a safe way to match both letter cases.
	lowerRaw := strings.ToLower(raw)
	if strings.Contains(lowerRaw, "%2f") {
		return "", errors.New("encoded slash in emby path")
	}
	if strings.Contains(lowerRaw, "%5c") {
		return "", errors.New("encoded backslash in emby path")
	}

	// Exactly one percent-decode. A literal %2f that emerges here is acceptable filename
	// text and is intentionally NOT re-scanned.
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", errors.New("invalid percent-encoding in emby path")
	}

	// Fold Windows separators so the segment / prefix / drive checks see one separator.
	p := strings.ReplaceAll(decoded, "\\", "/")

	for _, r := range p {
		if r < 0x20 {
			return "", errors.New("control character in emby path")
		}
		switch r {
		case '∕', '⁄', '／': // ∕ ⁄ ／
			return "", errors.New("unicode slash look-alike in emby path")
		}
	}

	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", errors.New("parent-directory segment in emby path")
		}
	}

	// Windows drive prefix, e.g. "C:/media/...". The backslash fold above means a path
	// like "C:\\..." already reads as "C:/...".
	if len(p) >= 2 && isASCIILetter(p[0]) && p[1] == ':' {
		return "", errors.New("windows drive prefix in emby path")
	}

	// UNC / authority-style double leading slash.
	if strings.HasPrefix(p, "//") {
		return "", errors.New("unc path in emby path")
	}

	return p, nil
}

// MatchPath normalizes rawPath and resolves it against rules, returning the longest
// segment-boundary prefix match. It propagates any NormalizeEmbyPath error. When no rule
// matches it returns ErrNoMapping. A matched rule whose EchoRelPrefix is itself unsafe
// (absolute, "..", or UNC-like) is rejected with an error rather than silently honored.
func MatchPath(rules []MappingRule, rawPath string) (MappingResult, error) {
	norm, err := NormalizeEmbyPath(rawPath)
	if err != nil {
		return MappingResult{}, err
	}

	// Pick the longest matching prefix regardless of rule order. matchLen records the
	// matched prefix length measured on the (possibly folded) comparison value, which for
	// the supported cases equals the byte length of the matched portion of norm.
	best := -1
	bestLen := -1
	for i := range rules {
		mLen, ok := prefixMatchLen(norm, rules[i].PrefixNorm, rules[i].CaseSensitive)
		if !ok {
			continue
		}
		if mLen > bestLen {
			bestLen = mLen
			best = i
		}
	}
	if best < 0 {
		return MappingResult{}, ErrNoMapping
	}

	rule := rules[best]
	if err := validateEchoRelPrefix(rule.EchoRelPrefix); err != nil {
		return MappingResult{}, err
	}

	// Suffix after the matched prefix, with any leading separator dropped, keeping the
	// original (input) casing.
	suffix := strings.TrimPrefix(norm[bestLen:], "/")

	rel := joinRel(rule.EchoRelPrefix, suffix)

	return MappingResult{
		MappingID: rule.ID,
		LibraryID: rule.LibraryID,
		RelPath:   rel,
		PathNorm:  norm,
	}, nil
}

// prefixMatchLen reports whether prefix matches norm at a segment boundary and, if so, the
// number of bytes of norm consumed by the prefix. A match requires norm == prefix or norm
// to continue with '/' immediately after the prefix, so "/media" matches "/media/x" but
// not "/media2/x". When caseSensitive is false the comparison is case-folded, but the
// returned length is always measured against norm so the caller slices the original casing.
func prefixMatchLen(norm, prefix string, caseSensitive bool) (int, bool) {
	if len(prefix) > len(norm) {
		return 0, false
	}
	head := norm[:len(prefix)]
	if caseSensitive {
		if head != prefix {
			return 0, false
		}
	} else {
		if !strings.EqualFold(head, prefix) {
			return 0, false
		}
	}
	if len(norm) == len(prefix) {
		return len(prefix), true
	}
	if norm[len(prefix)] == '/' {
		return len(prefix), true
	}
	return 0, false
}

// validateEchoRelPrefix rejects an EchoRelPrefix that would let the mapping escape the Echo
// resource root: an absolute path, any ".." segment, or a UNC-like double leading slash.
func validateEchoRelPrefix(p string) error {
	if p == "" {
		return nil
	}
	if strings.HasPrefix(p, "//") {
		return errors.New("unc echo_rel_prefix")
	}
	if strings.HasPrefix(p, "/") {
		return errors.New("absolute echo_rel_prefix")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return errors.New("parent-directory segment in echo_rel_prefix")
		}
	}
	return nil
}

// joinRel joins a (validated) EchoRelPrefix with the suffix into a single '/'-separated
// relative path with no leading slash. An empty prefix yields the suffix unchanged; an
// empty suffix yields the prefix unchanged.
func joinRel(prefix, suffix string) string {
	switch {
	case prefix == "":
		return suffix
	case suffix == "":
		return prefix
	default:
		return prefix + "/" + suffix
	}
}

// isASCIILetter reports whether b is an ASCII letter, used only for the drive-prefix check.
func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
