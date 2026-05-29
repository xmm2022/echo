// Command mock115share2cas is a stand-in for the upstream 115share2cas producer,
// used by the integration producer test. It ignores every share/auth flag Echo
// injects and simply writes a fixed CAS tree + manifest to the --out / --manifest
// paths, then exits 0 — mimicking a successful producer run that emits a local
// CAS tree. It lives under testdata/ so the normal `go build ./...` skips it; the
// test builds it explicitly.
package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	out := flagValue("--out")
	manifest := flagValue("--manifest")
	if out == "" || manifest == "" {
		fmt.Fprintln(os.Stderr, "mock115share2cas: --out and --manifest are required")
		os.Exit(2)
	}

	const relPath = "producer-movie.mkv"
	content := []byte("mock producer payload for a 115 share")

	casFile := filepath.Join(out, relPath+".cas")
	if err := os.MkdirAll(filepath.Dir(casFile), 0o750); err != nil {
		fail(err)
	}
	if err := os.WriteFile(casFile, content, 0o644); err != nil {
		fail(err)
	}

	sum := sha1.Sum(content)
	line, err := json.Marshal(map[string]any{
		"rel_path": relPath,
		"name":     relPath,
		"size":     len(content),
		"sha1":     hex.EncodeToString(sum[:]),
		"provider": "115",
	})
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(manifest, append(line, '\n'), 0o644); err != nil {
		fail(err)
	}
}

// flagValue scans argv for "--name value" or "--name=value", ignoring all other
// flags (Echo passes share_url/mode/etc. that this mock does not consume).
func flagValue(name string) string {
	args := os.Args[1:]
	prefix := name + "="
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			return args[i+1]
		}
		if len(args[i]) > len(prefix) && args[i][:len(prefix)] == prefix {
			return args[i][len(prefix):]
		}
	}
	return ""
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mock115share2cas:", err)
	os.Exit(1)
}
