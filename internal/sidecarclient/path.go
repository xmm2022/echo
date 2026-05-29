package sidecarclient

import (
	"path"
	"strings"
)

func sidecarPath(storageMount string, elems ...string) string {
	parts := make([]string, 0, len(elems)+1)
	if storageMount != "" {
		parts = append(parts, storageMount)
	}
	parts = append(parts, elems...)
	joined := path.Join(parts...)
	if joined == "." {
		return "/"
	}
	if !strings.HasPrefix(joined, "/") {
		return "/" + joined
	}
	return joined
}
