package pathsafe

import (
	"fmt"
	"path/filepath"
	"strings"
)

const maxRelPathLen = 4096

func ValidateRelPath(rel string) error {
	_, err := normalizeRelPath(rel)
	return err
}

func normalizeRelPath(rel string) (string, error) {
	cleaned, err := cleanRelPath(rel)
	if err != nil {
		return "", err
	}
	if cleaned != rel {
		cleaned, err = cleanRelPath(cleaned)
	}
	return cleaned, err
}

func cleanRelPath(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("relative path is empty")
	}
	if len(rel) > maxRelPathLen {
		return "", fmt.Errorf("relative path is longer than %d bytes", maxRelPathLen)
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("relative path must not be absolute")
	}
	if strings.Contains(rel, `\`) {
		return "", fmt.Errorf("relative path must not contain backslash")
	}
	if hasWindowsDrive(rel) {
		return "", fmt.Errorf("relative path must not contain windows drive prefix")
	}
	for _, r := range rel {
		if r < 0x20 {
			return "", fmt.Errorf("relative path contains control character")
		}
	}
	if containsParentSegment(rel) {
		return "", fmt.Errorf("relative path must not contain parent directory segment")
	}

	cleaned := filepath.Clean(rel)
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("relative path cleans to empty path")
	}
	return cleaned, nil
}

func containsParentSegment(p string) bool {
	for _, segment := range strings.Split(p, string(filepath.Separator)) {
		if segment == ".." {
			return true
		}
	}
	return false
}

func hasWindowsDrive(p string) bool {
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		return true
	}
	for i := 0; i+2 < len(p); i++ {
		if p[i] == filepath.Separator && isASCIILetter(p[i+1]) && p[i+2] == ':' {
			return true
		}
	}
	return false
}

func isASCIILetter(b byte) bool {
	return ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z')
}
