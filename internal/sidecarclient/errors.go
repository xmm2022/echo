package sidecarclient

import (
	"errors"
	"fmt"
)

var (
	ErrSidecarUnreachable   = errors.New("sidecar unreachable")
	ErrSidecarVersionTooOld = errors.New("sidecar version older than required")
	ErrStorageNotFound      = errors.New("storage not registered on sidecar")
	ErrCASRestoreFailed     = errors.New("sidecar refused or failed CAS restore")
	ErrLinkNotAvailable     = errors.New("sidecar returned no direct link")
)

type SidecarHTTPError struct {
	StatusCode int
	Method     string
	URL        string
}

func (e *SidecarHTTPError) Error() string {
	return fmt.Sprintf("sidecar HTTP %s %s returned %d", e.Method, e.URL, e.StatusCode)
}
