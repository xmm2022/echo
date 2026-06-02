// Package secrets centralizes secret references used by configuration,
// discovery sources, and producer adapters.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Resolve resolves a ref:relative/path reference under root. It follows
// symlinks, rejects symlink escapes, and only accepts regular files.
func Resolve(root, ref string) (string, error) {
	rel, err := parseFileRef(ref)
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", errors.New("secret root is empty")
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve secret root: %w", err)
	}
	candidate := filepath.Join(realRoot, rel)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve secret ref: %w", err)
	}
	if err := ensureUnderRoot(realRoot, resolved); err != nil {
		return "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat secret ref: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("secret ref is not a regular file")
	}
	return resolved, nil
}

// ResolveEnv resolves an env:NAME reference from the process environment.
func ResolveEnv(ref string) (string, error) {
	name, err := parseEnvRef(ref)
	if err != nil {
		return "", err
	}
	value, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("environment variable %q is not set", name)
	}
	return value, nil
}

// ValidateRef validates env: refs syntactically and ref: refs structurally
// without requiring the referenced file to exist.
func ValidateRef(root, ref string) error {
	if strings.HasPrefix(ref, "env:") {
		_, err := parseEnvRef(ref)
		return err
	}
	if root == "" {
		return errors.New("secret root is empty")
	}
	_, err := parseFileRef(ref)
	return err
}

// RedactRef redacts non-empty secret references for logs and UI previews.
func RedactRef(ref string) string {
	if ref == "" {
		return ""
	}
	return "[REDACTED]"
}

func parseEnvRef(ref string) (string, error) {
	if !strings.HasPrefix(ref, "env:") {
		return "", errors.New("secret env ref must start with env:")
	}
	name := strings.TrimPrefix(ref, "env:")
	if !envNamePattern.MatchString(name) {
		return "", errors.New("secret env ref has invalid variable name")
	}
	return name, nil
}

func parseFileRef(ref string) (string, error) {
	if !strings.HasPrefix(ref, "ref:") {
		return "", errors.New("secret file ref must start with ref:")
	}
	rel := strings.TrimPrefix(ref, "ref:")
	if rel == "" {
		return "", errors.New("secret file ref path is empty")
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("secret file ref path must be relative")
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("secret file ref escapes root")
	}
	return clean, nil
}

func ensureUnderRoot(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("compare secret ref path: %w", err)
	}
	if rel == "." || rel == "" {
		return errors.New("secret ref must not resolve to root")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("secret ref escapes root")
	}
	return nil
}
