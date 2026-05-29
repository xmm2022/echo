package restore

import (
	"net/http"

	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store/queries"
)

// streamForwardHeaders is the request-header allowlist proxied to the sidecar
// (spec §4). Everything else — notably Cookie and Authorization — is dropped so
// it never reaches the upstream source.
var streamForwardHeaders = []string{
	"Range",
	"If-Range",
	"If-Modified-Since",
	"If-None-Match",
	"User-Agent",
}

// ForwardHeaders copies only the allowlisted request headers from src into a new
// header map.
func ForwardHeaders(src http.Header) http.Header {
	dst := http.Header{}
	for _, key := range streamForwardHeaders {
		for _, value := range src.Values(key) {
			dst.Add(key, value)
		}
	}
	return dst
}

// StreamRequestFor builds a sidecar StreamRequest targeting a copy, forwarding
// only the allowlisted client headers.
func StreamRequestFor(fc queries.FileCopy, clientHeader http.Header) sidecarclient.StreamRequest {
	return sidecarclient.StreamRequest{
		StorageMount: fc.StorageMount,
		RemotePath:   fc.RemotePath,
		Headers:      ForwardHeaders(clientHeader),
	}
}
