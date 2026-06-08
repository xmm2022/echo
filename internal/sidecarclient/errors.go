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

type SidecarErrorKind string

const (
	SidecarErrProtocol SidecarErrorKind = "protocol"
	// SidecarErrTransport is reserved for later Phase 0 work (task 0.4), which
	// maps dial/timeout transport failures into the typed-error switch.
	SidecarErrTransport SidecarErrorKind = "transport"
	// SidecarErrAPIEnvelope is reserved for later Phase 0 work (task 0.4) for
	// malformed/non-success OpenList envelopes that aren't otherwise classified.
	SidecarErrAPIEnvelope    SidecarErrorKind = "api_envelope"
	SidecarErrObjectMissing  SidecarErrorKind = "object_missing"
	SidecarErrStorageMissing SidecarErrorKind = "storage_missing"
	// SidecarErrStorageUnhealthy is reserved for later Phase 0 work (task 0.4),
	// which routes storage-health failures to account/storage handling.
	SidecarErrStorageUnhealthy SidecarErrorKind = "storage_unhealthy"
	SidecarErrAuthOrAccount    SidecarErrorKind = "auth_or_account"
	SidecarErrTransient        SidecarErrorKind = "transient"
)

type SidecarTypedError struct {
	Kind         SidecarErrorKind
	Operation    string
	HTTPStatus   int
	OpenListCode int
	SafeMessage  string
	// RawMessage is the unredacted original sidecar message (e.g. the raw /d/
	// HTML snippet). It must never be logged or serialized to clients; only
	// SafeMessage is safe for logs and API responses.
	RawMessage    string
	EvidenceClass string // json_envelope, http_status, html_snippet, transport
	Confidence    string // confirmed, suspect, low
}

func (e *SidecarTypedError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.SafeMessage != "" {
		return fmt.Sprintf("sidecar %s failed: %s", e.Operation, e.SafeMessage)
	}
	return fmt.Sprintf("sidecar %s failed: %s", e.Operation, e.Kind)
}

// sidecarEnvelopeError is the raw, operation-agnostic carrier returned by the
// shared envelope decode path when an OpenList response reports a non-success
// code (code != 200, including code == 0). Callers that have enough context
// (e.g. Link knows the operation and HTTP status) detect it via errors.As and
// re-wrap it into a *SidecarTypedError via classifySidecarMessage. Callers that
// only need a failure keep using its Error() string, which carries the message.
type sidecarEnvelopeError struct {
	Code    int
	Message string
}

func (e *sidecarEnvelopeError) Error() string {
	return fmt.Sprintf("sidecar api error: code=%d message=%s", e.Code, e.Message)
}

// OpenListEnvelopeErrorDetails extracts the safe code/message pair from an
// OpenList JSON envelope failure returned by this package.
func OpenListEnvelopeErrorDetails(err error) (code int, message string, ok bool) {
	var envelopeErr *sidecarEnvelopeError
	if !errors.As(err, &envelopeErr) {
		return 0, "", false
	}
	return envelopeErr.Code, envelopeErr.Message, true
}
