package pathsafe

import (
	"strings"
	"testing"
)

func TestValidateRelPathAcceptsSafePaths(t *testing.T) {
	tests := []string{
		"a",
		"a/b",
		"a//b",
		"./a",
		"a/./b",
		"space name/file.txt",
		" ",
		"a b/c d",
		"unicode/文件.txt",
		".hidden/file",
		"a.b/c-d_e",
		"0/1/2",
	}

	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			if err := ValidateRelPath(tt); err != nil {
				t.Fatalf("ValidateRelPath(%q) error = %v", tt, err)
			}
		})
	}
}

func TestValidateRelPathRejectsUnsafePaths(t *testing.T) {
	tests := []string{
		"",
		".",
		"./",
		"..",
		"../a",
		"a/../b",
		"a/b/../c",
		"/etc/passwd",
		"a\x00b",
		"a\x1fb",
		`a\b`,
		`C:\Windows`,
		"C:Windows",
		"a/C:/b",
		strings.Repeat("a", maxRelPathLen+1),
	}

	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			if err := ValidateRelPath(tt); err == nil {
				t.Fatalf("ValidateRelPath(%q) error = nil, want error", tt)
			}
		})
	}
}
