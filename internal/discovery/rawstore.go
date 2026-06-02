// Package discovery contains shared helpers for discovery source adapters and
// orchestration.
package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	storeq "github.com/xmm2022/echo/internal/store/queries"
)

type RawStoreConfig struct {
	Root     string
	MaxBytes int
}

type RawStore struct {
	cfg RawStoreConfig
}

type discoveredResourceWriter interface {
	UpsertDiscoveredResource(context.Context, storeq.UpsertDiscoveredResourceParams) (storeq.DiscoveredResource, error)
}

type SafeDiscoveredResourceWriter struct {
	inner discoveredResourceWriter
}

var (
	rawRefPattern       = regexp.MustCompile(`^raw:[0-9a-f]{64}$`)
	rawSecretStart      = regexp.MustCompile(`(?i)"?(password|pwd|receive_code|receiveCode|share_code|shareCode|share_password|sharePassword|api_hash|apiHash|api_key|apiKey|tmdb_key|tmdbKey|session_ref|sessionRef|session_path|sessionPath)"?\s*[:=]\s*`)
	sensitiveStringExpr = regexp.MustCompile(`(?i)"?(password|pwd|receive_code|receiveCode|share_code|shareCode|share_password|sharePassword|api_hash|apiHash|api_key|apiKey|tmdb_key|tmdbKey|session_ref|sessionRef|session_path|sessionPath)"?\s*[:=]`)
	errParsedJSONSecret = errors.New("parsed_json must contain only redacted, capped, normalized data")
)

var parsedJSONSecrets = map[string]struct{}{
	"receive_code":   {},
	"receiveCode":    {},
	"share_code":     {},
	"shareCode":      {},
	"share_password": {},
	"sharePassword":  {},
	"password":       {},
	"pwd":            {},
	"api_hash":       {},
	"apiHash":        {},
	"api_key":        {},
	"apiKey":         {},
	"tmdb_key":       {},
	"tmdbKey":        {},
	"session_ref":    {},
	"sessionRef":     {},
	"session_path":   {},
	"sessionPath":    {},
	"raw_text":       {},
	"rawText":        {},
}

// NewRawStore returns a raw discovery payload store.
func NewRawStore(cfg RawStoreConfig) *RawStore {
	return &RawStore{cfg: cfg}
}

// NewSafeDiscoveredResourceWriter wraps the sqlc discovery candidate writer and
// enforces the parsed_json redaction contract before data reaches storage.
func NewSafeDiscoveredResourceWriter(inner discoveredResourceWriter) *SafeDiscoveredResourceWriter {
	return &SafeDiscoveredResourceWriter{inner: inner}
}

func (w *SafeDiscoveredResourceWriter) UpsertDiscoveredResource(ctx context.Context, arg storeq.UpsertDiscoveredResourceParams) (storeq.DiscoveredResource, error) {
	if err := ValidateParsedJSONForStorage([]byte(arg.ParsedJson)); err != nil {
		return storeq.DiscoveredResource{}, err
	}
	return w.inner.UpsertDiscoveredResource(ctx, arg)
}

// Put stores raw source text under a content-addressed ref and returns a
// redacted/capped preview suitable for discovered_resources.raw_text_redacted.
func (s *RawStore) Put(ctx context.Context, sourceKey, externalKey string, raw []byte) (ref string, redacted string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if s.cfg.Root == "" {
		return "", "", errors.New("raw store root is empty")
	}
	if s.cfg.MaxBytes <= 0 {
		return "", "", errors.New("raw store MaxBytes must be positive")
	}
	sum := sha256.Sum256(raw)
	hexSum := hex.EncodeToString(sum[:])
	ref = "raw:" + hexSum

	path, err := s.pathForHex(hexSum)
	if err != nil {
		return "", "", err
	}
	if err := ensureRawPrefixDir(path); err != nil {
		return "", "", err
	}
	if err := rejectSymlinkLeaf(path); err != nil {
		return "", "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", "", fmt.Errorf("write raw payload: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return "", "", fmt.Errorf("write raw payload: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", "", fmt.Errorf("close raw payload: %w", err)
	}

	preview := redactRawText(string(raw))
	preview = capStringBytes(preview, s.cfg.MaxBytes)
	return ref, preview, nil
}

// Get returns at most maxBytes from a raw:<sha256> reference. HTTP callers must
// redact the returned bytes again before writing a response.
func (s *RawStore) Get(ctx context.Context, ref string, maxBytes int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.cfg.Root == "" {
		return nil, errors.New("raw store root is empty")
	}
	hexSum, err := parseRawRef(ref)
	if err != nil {
		return nil, err
	}
	path, err := s.pathForHex(hexSum)
	if err != nil {
		return nil, err
	}
	if err := validateRawPrefixDir(path); err != nil {
		return nil, err
	}
	if err := rejectSymlinkLeaf(path); err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open raw payload: %w", err)
	}
	defer f.Close()

	if maxBytes < 0 {
		return nil, errors.New("maxBytes must be non-negative")
	}
	limited := io.LimitReader(f, int64(maxBytes))
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read raw payload: %w", err)
	}
	return body, nil
}

// Prune removes raw payload files older than olderThanUnix.
func (s *RawStore) Prune(ctx context.Context, olderThanUnix int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.cfg.Root == "" {
		return errors.New("raw store root is empty")
	}
	return filepath.WalkDir(s.cfg.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.ModTime().Unix() < olderThanUnix {
			return os.Remove(path)
		}
		return nil
	})
}

// ValidateParsedJSONForStorage enforces the discovered_resources.parsed_json
// contract: adapters and orchestrators must store only redacted, capped,
// normalized data here. Raw source text and credentials belong in raw storage or
// dedicated sensitive columns, never in parsed_json.
func ValidateParsedJSONForStorage(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("parse parsed_json: %w", err)
	}
	return rejectParsedJSONSecrets(value)
}

func (s *RawStore) pathForHex(hexSum string) (string, error) {
	if len(hexSum) != 64 {
		return "", errors.New("raw ref hash must be sha256 hex")
	}
	root, err := filepath.Abs(s.cfg.Root)
	if err != nil {
		return "", fmt.Errorf("resolve raw store root: %w", err)
	}
	path := filepath.Join(root, hexSum[:2], hexSum)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("compare raw store path: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("raw ref escapes root")
	}
	return path, nil
}

func parseRawRef(ref string) (string, error) {
	if !rawRefPattern.MatchString(ref) {
		return "", errors.New("raw ref must be raw:<sha256>")
	}
	hexSum := strings.TrimPrefix(ref, "raw:")
	if strings.ContainsAny(hexSum, `/\`) {
		return "", errors.New("raw ref must not contain path separators")
	}
	return hexSum, nil
}

func redactRawText(text string) string {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err == nil {
		body, err := json.Marshal(redactRawJSONValue(value))
		if err == nil {
			return string(body)
		}
	}
	return redactRawFallbackText(text)
}

func capStringBytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}

func rejectParsedJSONSecrets(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if _, ok := parsedJSONSecrets[key]; ok {
				return fmt.Errorf("%w: sensitive key %q", errParsedJSONSecret, key)
			}
			if err := rejectParsedJSONSecrets(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := rejectParsedJSONSecrets(child); err != nil {
				return err
			}
		}
	case string:
		if containsSensitiveString(v) {
			return fmt.Errorf("%w: sensitive string value", errParsedJSONSecret)
		}
	}
	return nil
}

func rejectSymlinkLeaf(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("lstat raw payload: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("raw payload leaf must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return errors.New("raw payload leaf must be a regular file")
	}
	return nil
}

func ensureRawPrefixDir(path string) error {
	root := filepath.Dir(filepath.Dir(path))
	prefixDir := filepath.Dir(path)
	if err := validateRawRootDir(root); err != nil {
		return err
	}
	info, err := os.Lstat(prefixDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("lstat raw prefix dir: %w", err)
		}
		return finishRawPrefixDirCreate(prefixDir, os.Mkdir(prefixDir, 0o700))
	}
	return validateRawPrefixInfo(info)
}

func validateRawPrefixDir(path string) error {
	root := filepath.Dir(filepath.Dir(path))
	prefixDir := filepath.Dir(path)
	if err := validateRawRootDir(root); err != nil {
		return err
	}
	info, err := os.Lstat(prefixDir)
	if err != nil {
		return fmt.Errorf("lstat raw prefix dir: %w", err)
	}
	return validateRawPrefixInfo(info)
}

func finishRawPrefixDirCreate(prefixDir string, mkdirErr error) error {
	if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
		return fmt.Errorf("create raw prefix dir: %w", mkdirErr)
	}
	info, err := os.Lstat(prefixDir)
	if err != nil {
		return fmt.Errorf("lstat raw prefix dir: %w", err)
	}
	return validateRawPrefixInfo(info)
}

func validateRawRootDir(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("lstat raw store root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("raw store root must not be a symlink")
	}
	if !info.IsDir() {
		return errors.New("raw store root must be a directory")
	}
	return nil
}

func validateRawPrefixInfo(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("raw prefix dir must not be a symlink")
	}
	if !info.IsDir() {
		return errors.New("raw prefix path must be a directory")
	}
	return nil
}

func redactRawJSONValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			if isParsedJSONSecretKey(key) {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = redactRawJSONValue(child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = redactRawJSONValue(child)
		}
		return out
	case string:
		if containsSensitiveString(v) {
			return "[REDACTED]"
		}
	}
	return value
}

func redactRawFallbackText(text string) string {
	var out strings.Builder
	cursor := 0
	matches := rawSecretStart.FindAllStringIndex(text, -1)
	for _, match := range matches {
		if match[0] < cursor {
			continue
		}
		out.WriteString(text[cursor:match[0]])
		out.WriteString("[REDACTED]")
		cursor = scanSecretValueEnd(text, match[1])
	}
	out.WriteString(text[cursor:])
	return out.String()
}

func scanSecretValueEnd(text string, start int) int {
	if start >= len(text) {
		return start
	}
	quote := text[start]
	if quote == '"' || quote == '\'' {
		i := start + 1
		escaped := false
		for i < len(text) {
			ch := text[i]
			if escaped {
				escaped = false
				i++
				continue
			}
			if ch == '\\' {
				escaped = true
				i++
				continue
			}
			i++
			if ch == quote {
				return i
			}
		}
		return len(text)
	}
	i := start
	for i < len(text) {
		switch text[i] {
		case ' ', '\t', '\n', '\r', ',', '}':
			return i
		default:
			i++
		}
	}
	return i
}

func isParsedJSONSecretKey(key string) bool {
	_, ok := parsedJSONSecrets[key]
	return ok
}

func containsSensitiveString(value string) bool {
	lower := strings.ToLower(value)
	return sensitiveStringExpr.MatchString(value) ||
		strings.Contains(lower, "?password=") ||
		strings.Contains(lower, "&password=") ||
		strings.Contains(lower, "?pwd=") ||
		strings.Contains(lower, "&pwd=") ||
		strings.Contains(lower, "?receive_code=") ||
		strings.Contains(lower, "&receive_code=") ||
		strings.Contains(lower, "?receivecode=") ||
		strings.Contains(lower, "&receivecode=") ||
		strings.Contains(lower, "telegram/session") ||
		strings.Contains(lower, "session.json")
}
