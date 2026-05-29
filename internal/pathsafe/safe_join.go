package pathsafe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ResolveExistingUnderRoot(root, candidate string) (string, error) {
	rootEvaluated, err := validateRoot(root)
	if err != nil {
		return "", err
	}
	rootClean := filepath.Clean(root)

	final := candidate
	if final == "" {
		return "", fmt.Errorf("candidate path is empty")
	}
	if !filepath.IsAbs(final) {
		cleaned, err := normalizeRelPath(final)
		if err != nil {
			return "", err
		}
		final = filepath.Join(rootEvaluated, cleaned)
	} else if containsParentSegment(final) {
		return "", fmt.Errorf("candidate path must not contain parent directory segment")
	}

	info, err := os.Lstat(final)
	if err != nil {
		return "", fmt.Errorf("lstat candidate path: %w", err)
	}
	if isSymlink(info) {
		return "", fmt.Errorf("candidate path is a symlink")
	}
	if err := ensureExistingNoSymlinkUnderRoot(rootEvaluated, rootClean, final); err != nil {
		return "", err
	}

	evaluated, err := filepath.EvalSymlinks(final)
	if err != nil {
		return "", fmt.Errorf("eval candidate symlinks: %w", err)
	}
	if err := ensureUnderRoot(rootEvaluated, evaluated); err != nil {
		return "", err
	}
	return evaluated, nil
}

func PrepareOutputUnderRoot(root, rel string, suffix string) (string, error) {
	rootEvaluated, err := validateRoot(root)
	if err != nil {
		return "", err
	}
	cleanedRel, err := normalizeRelPath(rel)
	if err != nil {
		return "", err
	}
	if err := validateSuffix(suffix); err != nil {
		return "", err
	}

	outputRel := cleanedRel + suffix
	parentRel := filepath.Dir(outputRel)
	if parentRel != "." {
		if _, err := ensureParents(rootEvaluated, parentRel, 0o755); err != nil {
			return "", err
		}
	}

	final := filepath.Join(rootEvaluated, outputRel)
	info, err := os.Lstat(final)
	if err == nil {
		if isSymlink(info) {
			return "", fmt.Errorf("output path is a symlink")
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("output path exists and is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("lstat output path: %w", err)
	}
	return final, nil
}

func PrepareNewDirUnderRoot(root, rel string) (string, error) {
	rootEvaluated, err := validateRoot(root)
	if err != nil {
		return "", err
	}
	cleanedRel, err := normalizeRelPath(rel)
	if err != nil {
		return "", err
	}

	parentRel := filepath.Dir(cleanedRel)
	if parentRel != "." {
		if _, err := ensureParents(rootEvaluated, parentRel, 0o755); err != nil {
			return "", err
		}
	}

	final := filepath.Join(rootEvaluated, cleanedRel)
	if _, err := os.Lstat(final); err == nil {
		return "", fmt.Errorf("new directory path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("lstat new directory path: %w", err)
	}
	if err := os.Mkdir(final, 0o750); err != nil {
		return "", fmt.Errorf("mkdir new directory path: %w", err)
	}
	evaluated, err := filepath.EvalSymlinks(final)
	if err != nil {
		return "", fmt.Errorf("eval new directory symlinks: %w", err)
	}
	if err := ensureUnderRoot(rootEvaluated, evaluated); err != nil {
		return "", err
	}
	return evaluated, nil
}

func SafeJoinUnderLibrary(libRoot, rel string) (string, error) {
	return PrepareOutputUnderRoot(libRoot, rel, ".echo")
}

func validateRoot(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("root path is empty")
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("root path must be absolute")
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("stat root path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("root path is not a directory")
	}
	evaluated, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("eval root symlinks: %w", err)
	}
	return evaluated, nil
}

func ensureParents(rootEvaluated, rel string, mode os.FileMode) (string, error) {
	if err := ValidateRelPath(rel); err != nil {
		return "", err
	}

	current := rootEvaluated
	for _, segment := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		next := filepath.Join(current, segment)
		info, err := os.Lstat(next)
		if err == nil {
			if isSymlink(info) {
				return "", fmt.Errorf("parent path %q is a symlink", next)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("parent path %q is not a directory", next)
			}
		} else if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(next, mode); err != nil {
				return "", fmt.Errorf("mkdir parent path %q: %w", next, err)
			}
			info, err = os.Lstat(next)
			if err != nil {
				return "", fmt.Errorf("lstat created parent path %q: %w", next, err)
			}
			if isSymlink(info) || !info.IsDir() {
				return "", fmt.Errorf("created parent path %q is unsafe", next)
			}
		} else {
			return "", fmt.Errorf("lstat parent path %q: %w", next, err)
		}

		evaluated, err := filepath.EvalSymlinks(next)
		if err != nil {
			return "", fmt.Errorf("eval parent symlinks %q: %w", next, err)
		}
		if err := ensureUnderRoot(rootEvaluated, evaluated); err != nil {
			return "", err
		}
		current = next
	}
	return current, nil
}

func ensureExistingNoSymlinkUnderRoot(rootEvaluated, rootClean, final string) error {
	for _, root := range []string{rootEvaluated, rootClean} {
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, final)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if rel == "." {
			return nil
		}

		current := root
		for _, segment := range strings.Split(rel, string(filepath.Separator)) {
			next := filepath.Join(current, segment)
			info, err := os.Lstat(next)
			if err != nil {
				return fmt.Errorf("lstat candidate path segment %q: %w", next, err)
			}
			if isSymlink(info) {
				return fmt.Errorf("candidate path segment %q is a symlink", next)
			}
			current = next
		}
		return nil
	}
	return fmt.Errorf("candidate path is not under root before symlink evaluation")
}

func ensureUnderRoot(rootEvaluated, candidate string) error {
	rel, err := filepath.Rel(rootEvaluated, candidate)
	if err != nil {
		return fmt.Errorf("compute relative path under root: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes root")
	}
	return nil
}

func validateSuffix(suffix string) error {
	if suffix == "" {
		return fmt.Errorf("suffix is empty")
	}
	if strings.Contains(suffix, string(filepath.Separator)) || strings.Contains(suffix, `\`) {
		return fmt.Errorf("suffix must not contain path separator")
	}
	for _, r := range suffix {
		if r < 0x20 {
			return fmt.Errorf("suffix contains control character")
		}
	}
	return nil
}

func isSymlink(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
